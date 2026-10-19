package orders

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"marketplace/internal/api"
)

func decisionProxy(t *testing.T, base *pgxpool.Pool, after bool) *pgxpool.Pool {
	t.Helper()
	conf := base.Config()
	upstream := net.JoinHostPort(conf.ConnConfig.Host, fmt.Sprint(conf.ConnConfig.Port))
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			client, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				server, err := net.Dial("tcp", upstream)
				if err != nil {
					_ = client.Close()
					return
				}
				defer client.Close()
				defer server.Close()
				length := make([]byte, 4)
				if _, err = io.ReadFull(client, length); err != nil {
					return
				}
				n := int(binary.BigEndian.Uint32(length))
				if n < 4 || n > 65536 {
					return
				}
				startup := make([]byte, n)
				copy(startup, length)
				if _, err = io.ReadFull(client, startup[4:]); err != nil {
					return
				}
				if _, err = server.Write(startup); err != nil {
					return
				}
				var decision atomic.Bool
				go func() {
					defer client.Close()
					defer server.Close()
					for {
						packet, err := postgresPacket(client)
						if err != nil {
							return
						}
						if packet[0] == 'Q' && strings.Contains(string(packet[5:]), "UPDATE transaction_journal SET decision='COMMIT'") {
							decision.Store(true)
							if !after {
								return
							}
						}
						if _, err = server.Write(packet); err != nil {
							return
						}
					}
				}()
				for {
					packet, err := postgresPacket(server)
					if err != nil {
						return
					}
					if after && decision.Load() && packet[0] == 'Z' {
						return
					}
					if _, err = client.Write(packet); err != nil {
						return
					}
				}
			}()
		}
	}()
	addr := listener.Addr().(*net.TCPAddr)
	conf.ConnConfig.Host = "127.0.0.1"
	conf.ConnConfig.Port = uint16(addr.Port)
	conf.ConnConfig.TLSConfig = nil
	conf.ConnConfig.Fallbacks = nil
	conf.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
	conf.MaxConns = 1
	conf.MinConns = 0
	db, err := pgxpool.NewWithConfig(context.Background(), conf)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	return db
}
func postgresPacket(c net.Conn) ([]byte, error) {
	header := make([]byte, 5)
	if _, err := io.ReadFull(c, header); err != nil {
		return nil, err
	}
	n := int(binary.BigEndian.Uint32(header[1:]))
	if n < 4 || n > 16*1024*1024 {
		return nil, fmt.Errorf("invalid pg packet length %d", n)
	}
	packet := make([]byte, n+1)
	copy(packet, header)
	_, err := io.ReadFull(c, packet[5:])
	return packet, err
}

func TestLostCommitDecisionReplyBeforeAndAfterDurableWrite(t *testing.T) {
	for _, after := range []bool{false, true} {
		t.Run(map[bool]string{false: "decision never reached database", true: "decision persisted reply lost"}[after], func(t *testing.T) {
			f := newFixture(t)
			p := f.product("10", 4)
			proxy := decisionProxy(t, f.db, after)
			s := NewService(proxy, f.svc.productService, 0)
			_, err := s.CreateOrder(f.ctx, f.user, &api.OrderCreate{Items: []api.OrderItemRequest{item(p, 2)}})
			mustCode(t, err, "TRANSACTION_PENDING")
			var gid, decision string
			if err = f.db.QueryRow(f.ctx, "SELECT id,decision FROM transaction_journal WHERE NOT completed ORDER BY created_at DESC LIMIT 1").Scan(&gid, &decision); err != nil {
				t.Fatal(err)
			}
			want := "PREPARING"
			if after {
				want = "COMMIT"
			}
			if decision != want {
				t.Fatalf("decision=%s want=%s", decision, want)
			}
			if err = f.svc.Recover(f.ctx); err != nil {
				t.Fatal(err)
			}
			var n int
			if err = f.db.QueryRow(f.ctx, "SELECT count(*) FROM orders WHERE user_id=$1", f.user.ID).Scan(&n); err != nil {
				t.Fatal(err)
			}
			expected := 0
			stock := int32(4)
			if after {
				expected = 1
				stock = 2
			}
			if n != expected {
				t.Fatalf("orders=%d want=%d", n, expected)
			}
			f.stock(p.ID, stock)
			assertNoPrepared(t, f)
		})
	}
}
