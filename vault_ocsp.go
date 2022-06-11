/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

package main

import (
	"bytes"
	"crypto"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/json"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"io/ioutil"
	"math/big"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/cloudflare/cfssl/log"
	cfocsp "github.com/cloudflare/cfssl/ocsp"
	"github.com/hashicorp/vault/api"
	"golang.org/x/crypto/ocsp"
)

func main() {
	var autoMount = flag.Uint("automount", 0, "if present, PKI mount will be extracted from request URL using the number of levels specified")
	var pkiMount = flag.String("pkimount", "pki", "vault PKI mount to use")
	var serverAddr = flag.String("serverAddr", ":8080", "Server IP and Port to use")
	var responderCertFile = flag.String("responderCert", "", "OCSP responder signing certificate file")
	var responderKeyFile = flag.String("responderKey", "", "OCSP responder signing private key file")

	flag.Parse()

	if *responderKeyFile == "" || *responderCertFile == "" {
		log.Critical("You have to specify a responder key and certificate")
		flag.Usage()
		os.Exit(1)
	}

	responderCert, err := parseResponderCertificate(*responderCertFile)
	if err != nil {
		log.Criticalf("Error, no responder certificate: %v", err)
		os.Exit(1)
	}
	responderKey, err := parseResponderKey(*responderKeyFile)
	if err != nil {
		log.Criticalf("Error, no responder key: %v", err)
		os.Exit(1)
	}

	if *autoMount == 0 {
		// Original (default) behavior with single PKI mount
		vaultSource, err := NewVaultSource(*pkiMount, responderCert, &responderKey, nil)
		if err != nil {
			log.Criticalf("vault source initialization failed: %v", err)
			os.Exit(1)
		}
		http.Handle("/", cfocsp.NewResponder(vaultSource, nil))
	} else {
		// Use AutoVaultResponder shim to handle OCSP requests for different PKI mount points
		http.Handle("/", NewAutoVaultResponder(*autoMount, responderCert, &responderKey))
	}

	server := &http.Server{
		Addr: *serverAddr,
	}
	if err := server.ListenAndServe(); err != nil {
		log.Criticalf("ListenAndServe failed: %v", err)
	}
}

type AutoVaultResponder struct {
	levels        uint
	responders    map[string]*cfocsp.Responder
	responderCert *x509.Certificate
	responderKey  *crypto.Signer
}

func NewAutoVaultResponder(levels uint, responderCert *x509.Certificate, responderKey *crypto.Signer) *AutoVaultResponder {
	return &AutoVaultResponder{
		levels:        levels,
		responders:    make(map[string]*cfocsp.Responder),
		responderCert: responderCert,
		responderKey:  responderKey,
	}
}

func (r AutoVaultResponder) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	log.Infof("Incoming %s request on: %s", request.Method, request.URL.Path)

	// Basic fail-early sanity checks
	var parts = strings.Split(strings.Trim(request.URL.Path, "/"), "/")
	switch request.Method {
	case "GET":
		// On GET requests we expect to have "parts" > levels as the data is sent
		// in base64 form via the path itself.
		if len(parts) <= int(r.levels) {
			log.Errorf("not enough parts (%d) in URL path to fulfill GET request with %d level(s) for PKI mount", len(parts), r.levels)
			response.WriteHeader(http.StatusNotFound)
			return
		}
	case "POST":
		// On POST requests we should have the same exact amount of "parts" from
		// the path as the ones we expect.
		if len(parts) != int(r.levels) {
			log.Errorf("unexpected number of parts (%d) in URL path to fulfill POST request with %d level(s) for PKI mount", len(parts), r.levels)
			response.WriteHeader(http.StatusNotFound)
			return
		}
	default:
		response.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	var pkiMount = strings.Join(parts[:r.levels], "/")
	request.URL.Path = strings.Join(parts[r.levels:], "/")
	log.Debugf("Extracted this PKI mount using %d levels from the request path: %s", r.levels, pkiMount)
	log.Debugf("Final request path for OCSP responder is: %s", request.URL.Path)

	var responder = r.responders[pkiMount]

	if responder == nil {
		// Setup a new VaultSource for this path
		log.Debugf("Creating Vault source for PKI mount '%s'", pkiMount)
		vaultSource, err := NewVaultSource(pkiMount, r.responderCert, r.responderKey, nil)
		if err != nil {
			log.Errorf("vault source initialization failed for mount '%s': %v", pkiMount, err)
		} else {
			log.Debugf("Successfully created Vault source for PKI mount '%s', now creating mount-specific OCSP responder", pkiMount)
			responder = cfocsp.NewResponder(vaultSource, nil)
			r.responders[pkiMount] = responder
		}
	}
	if responder != nil {
		// Call the responder to do the actual work
		log.Debugf("Delegating work to mount-specific OCSP responder")
		responder.ServeHTTP(response, request)
		return
	}
	// Invalid request path
	response.WriteHeader(http.StatusNotFound)
}

func parseResponderKey(responderKeyFile string) (responderKey crypto.Signer, err error) {
	pemBytes, err := ioutil.ReadFile(responderKeyFile)
	if err != nil {
		err = fmt.Errorf("could not read responder key data: %v", err)
		return
	}
	pemBlock, _ := pem.Decode(pemBytes)
	if pemBlock == nil {
		err = errors.New("could not decode PEM data for responder key")
		return
	}
	responderKey, err = x509.ParsePKCS1PrivateKey(pemBlock.Bytes)
	if err != nil {
		err = fmt.Errorf("could not parse PKCS1 formatted RSA key: %v", err)
		return
	}
	return
}

func parseResponderCertificate(responderCertFile string) (responderCert *x509.Certificate, err error) {
	pemBytes, err := ioutil.ReadFile(responderCertFile)
	if err != nil {
		err = fmt.Errorf("could not read responder certificate data: %v", err)
		return
	}
	pemBlock, _ := pem.Decode(pemBytes)
	if pemBlock == nil {
		err = errors.New("could not decode PEM data for responder certificate")
		return
	}
	responderCert, err = x509.ParseCertificate(pemBlock.Bytes)
	if err != nil {
		err = fmt.Errorf("could not parse responder certificate: %v", err)
		return
	}
	return
}

type VaultSource struct {
	pkiMount             string
	cached               map[string][]byte
	vaultClient          *api.Client
	caCertificate        *x509.Certificate
	responderCertificate *x509.Certificate
	responderKey         *crypto.Signer
}

func NewVaultSource(pkiMount string, responderCertificate *x509.Certificate, responderKey *crypto.Signer, config *api.Config) (*VaultSource, error) {
	client, err := api.NewClient(config)
	if err != nil {
		return nil, fmt.Errorf("error initializing vault client: %v", err)
	}
	vaultRequest := client.NewRequest(http.MethodGet, fmt.Sprintf("/v1/%s/ca", pkiMount))
	vaultResponse, err := client.RawRequest(vaultRequest)
	if err != nil {
		return nil, fmt.Errorf("error getting CA certificate from vault: %v", err)
	}
	caCertificateBytes, err := ioutil.ReadAll(vaultResponse.Body)
	if err != nil {
		return nil, fmt.Errorf("could not read CA certificate data from vault: %v", err)
	}
	caCertificate, err := x509.ParseCertificate(caCertificateBytes)
	if err != nil {
		return nil, fmt.Errorf("could not parse CA certificate data from vault: %v", err)
	}
	log.Infof("Found CA certificate %v", caCertificate.Subject.CommonName)
	vaultSource := &VaultSource{
		pkiMount:             pkiMount,
		vaultClient:          client,
		caCertificate:        caCertificate,
		responderCertificate: responderCertificate,
		responderKey:         responderKey,
		cached:               make(map[string][]byte),
	}
	return vaultSource, nil
}

func (source VaultSource) buildCAHash(algorithm crypto.Hash) (issuerHash []byte, err error) {
	h := algorithm.New()
	var publicKeyInfo struct {
		Algorithm pkix.AlgorithmIdentifier
		PublicKey asn1.BitString
	}
	if _, err := asn1.Unmarshal(source.caCertificate.RawSubjectPublicKeyInfo, &publicKeyInfo); err != nil {
		log.Errorf("Error parsing CA certificate public key info: %v", err)
		return nil, err
	}
	h.Write(publicKeyInfo.PublicKey.RightAlign())
	issuerHash = h.Sum(nil)
	return issuerHash, nil
}

func (source VaultSource) Response(request *ocsp.Request) ([]byte, http.Header, error) {
	caHash, err := source.buildCAHash(request.HashAlgorithm)
	if err != nil {
		return nil, nil, fmt.Errorf("error building CA certificate hash with algorithm %d: %v", request.HashAlgorithm, err)
	}
	if !bytes.Equal(request.IssuerKeyHash, caHash) {
		return nil, nil, errors.New("request issuer key has does not match CA subject key hash")
	}

	cacheKey := request.SerialNumber.String()
	response, present := source.cached[cacheKey]
	if present {
		return response, nil, nil
	}
	vaultSerial := toVaultSerial(request.SerialNumber)
	log.Infof("OCSP request for serial %s\n", vaultSerial)
	vaultResponse, err := source.vaultClient.Logical().Read(
		fmt.Sprintf("%s/cert/%s", source.pkiMount, vaultSerial))
	if err != nil {
		return nil, nil, fmt.Errorf("error reading certificate information for %s from vault", vaultSerial)
	}
	revocationTime, found := vaultResponse.Data["revocation_time"]
	if !found {
		// no revocation time in data
		return response, nil, nil
	}
	switch revocationTime.(type) {
	case json.Number:
		revTime, err := revocationTime.(json.Number).Int64()
		if err != nil {
			return nil, nil, errors.New("could not convert revocation time to int64 value")
		}

		if revTime != 0 {
			log.Infof("Certificate with serial number %s is revoked", vaultSerial)
			response, err = source.buildRevokedResponse(request.SerialNumber, time.Unix(revTime, 0))
			if err != nil {
				return nil, nil, fmt.Errorf("could not build response %v", err)
			}
			source.cached[cacheKey] = response
			return response, nil, nil
		}

		certificateString, found := vaultResponse.Data["certificate"]
		if !found {
			// no certificate in data
			return response, nil, nil
		}
		certificateBytes, err := ioutil.ReadAll(strings.NewReader(certificateString.(string)))
		if err != nil {
			return nil, nil, fmt.Errorf("could not read certificate %v", err)
		}
		block, _ := pem.Decode(certificateBytes)
		if block == nil {
			return nil, nil, errors.New("could not decode PEM data")
		}
		certificate, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, nil, fmt.Errorf("could not parse certificate: %v", err)
		}
		if certificate.NotAfter.Before(time.Now()) {
			// certificate is expired, store unauthorized response in cache
			log.Infof("Certificate with serial %s expired at %s, returning unauthorized", vaultSerial, certificate.NotAfter)
			response = ocsp.UnauthorizedErrorResponse
			source.cached[cacheKey] = response
		} else {
			log.Infof("Certificate with serial %s is valid", vaultSerial)
			response, err = source.buildOkResponse(request.SerialNumber)
			if err != nil {
				return nil, nil, fmt.Errorf("could not build response %v", err)
			}
		}
		present = true
	}

	return response, nil, nil
}

func (source VaultSource) buildRevokedResponse(serialNumber *big.Int, revocationTime time.Time) ([]byte, error) {
	template := ocsp.Response{
		SerialNumber: serialNumber,
		Status:       ocsp.Revoked,
		ThisUpdate:   time.Now(),
		Certificate:  source.responderCertificate,
	}
	template.RevokedAt = revocationTime
	template.RevocationReason = ocsp.Unspecified
	return source.buildResponse(template)
}

func (source VaultSource) buildOkResponse(serialNumber *big.Int) (ocspResponse []byte, err error) {
	template := ocsp.Response{
		SerialNumber: serialNumber,
		Status:       ocsp.Good,
		ThisUpdate:   time.Now(),
		NextUpdate:   time.Now().Add(time.Hour),
		Certificate:  source.responderCertificate,
	}
	return source.buildResponse(template)
}

func (source VaultSource) buildResponse(template ocsp.Response) (ocspResponse []byte, err error) {
	ocspResponse, err = ocsp.CreateResponse(
		source.caCertificate, source.responderCertificate, template, *source.responderKey)
	return
}

func toVaultSerial(serial *big.Int) string {
	vaultSerial := serial.Text(16)
	if len(vaultSerial)%2 != 0 {
		vaultSerial = "0" + vaultSerial
	}
	serialParts := make([]string, len(vaultSerial)/2)
	for i := 0; i < len(vaultSerial)/2; i++ {
		serialParts[i] = vaultSerial[i*2 : i*2+2]
	}
	vaultSerial = strings.Join(serialParts, "-")
	return vaultSerial
}
