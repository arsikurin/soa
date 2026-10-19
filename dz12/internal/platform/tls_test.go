package platform

import (
	"context"
	"crypto/tls"
	"path/filepath"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

func TestCertificateIdentityAndMethodAuthorization(t *testing.T) {
	root := t.TempDir()
	if err := GenerateCertificates(root); err != nil {
		t.Fatal(err)
	}
	first, err := ClientTLS(filepath.Join(root, "orders"), "catalog")
	if err != nil {
		t.Fatal(err)
	}
	if err := GenerateCertificates(root); err != nil {
		t.Fatal(err)
	}
	server, err := ServerTLS(filepath.Join(root, "catalog"))
	if err != nil {
		t.Fatal(err)
	}
	listener, err := tls.Listen("tcp", "127.0.0.1:0", server)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	done := make(chan error, 1)
	go func() {
		c, e := listener.Accept()
		if e == nil {
			defer c.Close()
			e = c.(*tls.Conn).Handshake()
		}
		done <- e
	}()
	c, err := tls.Dial("tcp", listener.Addr().String(), first)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	ctx := peer.NewContext(context.Background(), &peer.Peer{AuthInfo: credentials.TLSInfo{State: tls.ConnectionState{PeerCertificates: c.ConnectionState().PeerCertificates}}})
	called := false
	handler := func(context.Context, any) (any, error) { called = true; return nil, nil }
	_, err = Authorize("users")(ctx, nil, &grpc.UnaryServerInfo{FullMethod: "/marketplace.v1.Users/LookupRole"}, handler)
	if err != nil || !called {
		t.Fatalf("catalog lookup denied: %v", err)
	}
	_, err = Authorize("orders")(ctx, nil, &grpc.UnaryServerInfo{FullMethod: "/marketplace.v1.Orders/CreateOrder"}, handler)
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("cross-service impersonation allowed: %v", err)
	}
	_, err = Authorize("orders")(context.Background(), nil, &grpc.UnaryServerInfo{FullMethod: "/marketplace.v1.Orders/CreateOrder"}, handler)
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("missing TLS accepted: %v", err)
	}
}

func TestServerRejectsClientWithoutCertificate(t *testing.T) {
	root := t.TempDir()
	if err := GenerateCertificates(root); err != nil {
		t.Fatal(err)
	}
	server, err := ServerTLS(filepath.Join(root, "catalog"))
	if err != nil {
		t.Fatal(err)
	}
	if server.ClientAuth != tls.RequireAndVerifyClientCert {
		t.Fatal("mTLS must require verified certificate")
	}
}
