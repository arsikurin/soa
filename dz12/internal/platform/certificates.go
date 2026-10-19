package platform

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"time"
)

var roles = []string{"gateway", "users", "catalog", "orders"}

func GenerateCertificates(root string) error {
	present := 0
	for _, role := range roles {
		for _, name := range []string{"ca.pem", "cert.pem", "key.pem"} {
			_, err := os.Stat(filepath.Join(root, role, name))
			if err == nil {
				present++
			} else if !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
	}
	if present == len(roles)*3 {
		return nil
	}
	if present != 0 {
		return errors.New("partial certificate set: restore the missing files or explicitly replace all certificate volumes")
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	serial := func() *big.Int {
		n, e := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
		if e != nil {
			panic(e)
		}
		return n
	}
	now := time.Now()
	ca := &x509.Certificate{SerialNumber: serial(), Subject: pkix.Name{CommonName: "Marketplace local CA"}, NotBefore: now.Add(-time.Minute), NotAfter: now.AddDate(2, 0, 0), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign}
	caDER, err := x509.CreateCertificate(rand.Reader, ca, ca, &key.PublicKey, key)
	if err != nil {
		return err
	}
	for _, role := range roles {
		leafKey, e := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if e != nil {
			return e
		}
		leaf := &x509.Certificate{SerialNumber: serial(), Subject: pkix.Name{CommonName: role}, DNSNames: []string{role}, NotBefore: ca.NotBefore, NotAfter: ca.NotAfter, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth}}
		der, e := x509.CreateCertificate(rand.Reader, leaf, ca, &leafKey.PublicKey, key)
		if e != nil {
			return e
		}
		private, e := x509.MarshalPKCS8PrivateKey(leafKey)
		if e != nil {
			return e
		}
		dir := filepath.Join(root, role)
		if e = os.MkdirAll(dir, 0755); e != nil {
			return e
		}
		for name, block := range map[string]*pem.Block{"ca.pem": {Type: "CERTIFICATE", Bytes: caDER}, "cert.pem": {Type: "CERTIFICATE", Bytes: der}, "key.pem": {Type: "PRIVATE KEY", Bytes: private}} {
			dest := filepath.Join(dir, name)
			mode := os.FileMode(0644)
			if name == "key.pem" {
				mode = 0600
			}
			if e = os.WriteFile(dest, pem.EncodeToMemory(block), mode); e != nil {
				return fmt.Errorf("write %s: %w", dest, e)
			}
			if os.Geteuid() == 0 {
				if e = os.Chown(dest, 10001, 10001); e != nil {
					return e
				}
			}
		}
	}
	return nil
}
