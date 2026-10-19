package platform

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

func tlsConfig(dir string) (*tls.Config, error) {
	cert, err := tls.LoadX509KeyPair(filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem"))
	if err != nil {
		return nil, fmt.Errorf("service certificate: %w", err)
	}
	ca, err := os.ReadFile(filepath.Join(dir, "ca.pem"))
	if err != nil {
		return nil, err
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(ca) {
		return nil, errors.New("invalid service CA")
	}
	return &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{cert}, RootCAs: roots, ClientCAs: roots}, nil
}
func ServerTLS(dir string) (*tls.Config, error) {
	c, e := tlsConfig(dir)
	if e == nil {
		c.ClientAuth = tls.RequireAndVerifyClientCert
	}
	return c, e
}
func ClientTLS(dir, server string) (*tls.Config, error) {
	c, e := tlsConfig(dir)
	if e == nil {
		c.ServerName = server
	}
	return c, e
}

func Authorize(service string) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, next grpc.UnaryHandler) (any, error) {
		p, ok := peer.FromContext(ctx)
		if !ok {
			return nil, status.Error(codes.Unauthenticated, "service certificate required")
		}
		auth, ok := p.AuthInfo.(credentials.TLSInfo)
		if !ok || len(auth.State.PeerCertificates) == 0 {
			return nil, status.Error(codes.Unauthenticated, "service certificate required")
		}
		caller := auth.State.PeerCertificates[0].Subject.CommonName
		known := caller == "gateway" || caller == "users" || caller == "catalog" || caller == "orders"
		allowed := false
		switch {
		case strings.HasPrefix(info.FullMethod, "/grpc.health.v1.Health/"):
			allowed = known
		case service == "users" && info.FullMethod == "/marketplace.v1.Users/LookupRole":
			allowed = caller == "catalog"
		case service == "catalog" && (info.FullMethod == "/marketplace.v1.Catalog/PrepareReservation" || info.FullMethod == "/marketplace.v1.Catalog/ResolveReservation"):
			allowed = caller == "orders"
		default:
			allowed = caller == "gateway"
		}
		if !allowed {
			return nil, status.Error(codes.PermissionDenied, "service is not allowed to call this method")
		}
		return next(ctx, req)
	}
}

func BoundRequests(limit int) grpc.UnaryServerInterceptor {
	slots := make(chan struct{}, limit)
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, next grpc.UnaryHandler) (any, error) {
		if strings.HasPrefix(info.FullMethod, "/grpc.health.v1.Health/") {
			return next(ctx, req)
		}
		select {
		case slots <- struct{}{}:
			defer func() { <-slots }()
		default:
			return nil, status.Error(codes.ResourceExhausted, "service is busy")
		}
		ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		return next(ctx, req)
	}
}
