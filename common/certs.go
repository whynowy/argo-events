package common

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"time"

	log "github.com/sirupsen/logrus"
)

const (
	organization = "ArgoProj"
)

// CreateCerts is used to generate a CA cert, a signed cert and a private key.
// Parameter - hosts define the DNS names
// Parameter - notAfter specifies the expiration date.
func CreateCerts(ctx context.Context, hosts []string, notAfter time.Time) (serverKey, serverCert, caCert []byte, err error) {
	caKey, caCertificate, caCertificatePEM, err := createCA(ctx, hosts, notAfter)
	if err != nil {
		log.Errorf("failed to create CA cert and key: %v", err)
		return nil, nil, nil, err
	}
	// Then create the private key for the serving cert
	serverPrivateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		log.Errorf("error generating private key, error: %v", err)
		return nil, nil, nil, err
	}
	serverCertTemplate, err := createCertTemplate(hosts, notAfter)
	if err != nil {
		log.Errorf("error generating server cert template: %v", err)
		return nil, nil, nil, err
	}
	_, serverCertPEM, err := createCert(serverCertTemplate, caCertificate, &serverPrivateKey.PublicKey, caKey)
	if err != nil {
		log.Errorf("error signing server certificate template: %v", err)
		return nil, nil, nil, err
	}
	privateKeyPEM := pem.EncodeToMemory(&pem.Block{
		Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(serverPrivateKey),
	})
	return privateKeyPEM, serverCertPEM, caCertificatePEM, nil
}

func createCertTemplate(hosts []string, notAfter time.Time) (*x509.Certificate, error) {
	serialNumberLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	serialNumber, err := rand.Int(rand.Reader, serialNumberLimit)
	if err != nil {
		log.Errorf("Failed to generate serial number: %v", err)
		return nil, err
	}

	tmpl := x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			Organization: []string{organization},
		},
		SignatureAlgorithm:    x509.SHA256WithRSA,
		NotBefore:             time.Now(),
		NotAfter:              notAfter,
		BasicConstraintsValid: true,
		DNSNames:              hosts,
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	return &tmpl, nil
}

func createCACertTemplate(hosts []string, notAfter time.Time) (*x509.Certificate, error) {
	caCert, err := createCertTemplate(hosts, notAfter)
	if err != nil {
		return nil, err
	}
	caCert.IsCA = true
	caCert.KeyUsage = x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature
	caCert.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth}
	return caCert, nil
}

func createCert(template, parent *x509.Certificate, pub interface{}, parentPriv interface{}) (*x509.Certificate, []byte, error) {
	certDER, err := x509.CreateCertificate(rand.Reader, template, parent, pub, parentPriv)
	if err != nil {
		log.Errorf("failed to create cert: %s", err)
		return nil, nil, err
	}
	cert, err := x509.ParseCertificate(certDER)
	if err != nil {
		log.Errorf("failed to parse cert: %s", err)
		return nil, nil, err
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	return cert, certPEM, nil
}

func createCA(ctx context.Context, hosts []string, notAfter time.Time) (*rsa.PrivateKey, *x509.Certificate, []byte, error) {
	rootKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		log.Errorf("failed to generate random root CA key: %v", err)
		return nil, nil, nil, err
	}
	rootCertTmpl, err := createCACertTemplate(hosts, notAfter)
	if err != nil {
		log.Errorf("failed to generate root CA cert: %v", err)
		return nil, nil, nil, err
	}
	rootCert, rootCertPEM, err := createCert(rootCertTmpl, rootCertTmpl, &rootKey.PublicKey, rootKey)
	if err != nil {
		log.Errorf("failed to sign the root CA cert: %v", err)
		return nil, nil, nil, err
	}
	return rootKey, rootCert, rootCertPEM, nil
}
