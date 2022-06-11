Vault OCSP with "Auto PKI Mount" feature
========================================

Vault OCSP provides OCSP support for
[Hashicorp Vault](https://www.vaultproject.io/)
[PKI backends](https://www.vaultproject.io/docs/secrets/pki/index.html)
it uses Vault to retrieve a CA certificate at startup and the
`cert/{serial}` API to fetch the revocation status of certificates.
Responses for revoked certificates are cached in memory.

Vault OCSP is based on Hashicorp's Vault API and OCSP code from [Cloudflare's PKI and TLS toolkit](https://cfssl.org/).

The original version of this code comes from [T-Systems-MMS](https://github.com/T-Systems-MMS/vault-ocsp) (**Thanks!**). It was modified to allow the responder to pickup the PKI mount from the HTTP request path. Right now this is done using a simple "levels" value using the `automount` parameter.

**STABILITY:**

For now this is still being tested and evaluated and should be considered a proof-of-concept. Your feedback is welcome.

**GOALS:**

* Allow sharing a single OCSP responder for multiple Vault PKI mounts.
* Allow addition of new PKI mounts without having to change anything on the OCSP responder (given that you follow some standard mount convention).
* Implement the feature with minimal changes to the original code.

**NON-GOALS:**

* Path validation: Like the original code, if requests paths do not make sense, the request will error out due to failure to initialize the Vault source. That said, I added very minimal checks when extracting/modifying the request path to fail faster in some cases.
* Support for namespaces. Right now you can only have a single value for `-automount` which makes having a PKI setup in both the Vault root namespace and other namespaces impossible without using multiple Vault OCSP instances. The original requirement was to handle multiple PKI mount points in the root namespace only.

**POSSIBLE IMPROVEMENTS:**

* Use a regular expression to extract PKI mount points from the request path (original requirement did not need that much freedom, but this might better fulfill some use cases and also allow support for namespaces).

License
-------

Vault OCSP is licensed under the Mozilla Public License 2.0.

The file `vendor/github.com/cloudflare/cfssl/ocsp/responder.go` is
copied from Cloudflare's cfssl repository and is licensed under cfssl's
BSD 2-clause "Simplified" License

Building Vault OCSP
-------------------

```bash
git clone https://github.com/mponton/vault-ocsp.git
cd vault-ocsp
go get
go build -o vault-ocsp
```

Running Vault OCSP
------------------

Vault OCSP is helpful:

```bash
./vault-ocsp -help
Usage of ./vault-ocsp:
  -automount uint
        if present, PKI mount will be extracted from request URL using the number of levels specified
  -pkimount string
        vault PKI mount to use (default "pki")
  -responderCert string
        OCSP responder signing certificate file
  -responderKey string
        OCSP responder signing private key file
  -serverAddr string
        Server IP and Port to use (default ":8080")
```

Vault OCSP supports the same environment variables as the Vault command
line interface. You will probably need to set `VAULT_ADDR`,
`VAULT_CACERT` and `VAULT_TOKEN` to use it.

When the command line argument `-automount` is specified and not `0` the argument `-pkimount` will be ignored. The value used with `-automount` tells Vault OCSP what parts of the URL path should be extracted as the Vault PKI mount point. For example, a value of `1` will extract a single level so, from a request path of `/pki1/OCSP_BASE64_DATA`, `pki1` will be extracted as the Vault PKI mount to use for this request and `OCSP_BASE64_DATA` will be passed on to the OCSP responder. A level value of `2` would handle, for example, a setup where all your PKI mounts are located under `/pki` (e.g. `/pki/foo`, `/pki/bar`, ...).

The command line arguments `-responderCert` and `-responderKey` are
mandatory and should point to a PEM encoded X.509 certificate file and
a corresponding PEM and PKCS#1 encoded RSA private key file.

The key can be generated using `openssl rsa` and the certificate should
be signed by a CA that is trusted by the OCSP clients that will query
the Vault OCSP instance.

Make Vault OCSP known to Vault
------------------------------

You can use the
[`/pki/config/urls` API](https://www.vaultproject.io/api/secret/pki/index.html#set-urls)
to define Vault OCSP as OCSP responder. You should use an OCSP URL that
will be reachable from your OCSP clients. If you want to make the OCSP
responder available via https itself you will need a reverse proxy like
nginx or Apache httpd in front of Vault OCSP.

When using the `-automount` feature, ensure you follow a proper convention for your PKI mounts as you can only specify one value for the number of levels to extract from the request path to use as PKI mount.
