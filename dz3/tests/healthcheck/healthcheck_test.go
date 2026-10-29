package healthcheck

import (
	"net"
	"os"
	"os/exec"
	"testing"
)

func TestCommandsAcceptCompleteListenAddresses(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	_, port, _ := net.SplitHostPort(listener.Addr().String())
	for _, test := range []struct{ command, variable, address string }{
		{"booking", "HTTP_ADDR", "127.0.0.1:" + port},
		{"flight", "GRPC_ADDR", "0.0.0.0:" + port},
	} {
		t.Run(test.command, func(t *testing.T) {
			command := exec.Command("go", "run", "../../cmd/"+test.command, "healthcheck")
			command.Env = append(os.Environ(), test.variable+"="+test.address)
			if output, err := command.CombinedOutput(); err != nil {
				t.Fatalf("healthcheck %s: %v\n%s", test.address, err, output)
			}
		})
	}
}
