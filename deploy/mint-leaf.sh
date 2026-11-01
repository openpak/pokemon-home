#!/usr/bin/env bash
# Mint the HOME leaf from the OpenPak CA: leaf + CA in one PEM, backdated to 2020 (a console with a
# clock that is behind rejects a not-yet-valid cert; NPLN lesson). Usage: mint-leaf.sh host1 [host2 ...]
# Run on the box in ~/openpak/deploy. Writes keys/pokemon-home.{key,pem}. Needs python3 cryptography.
set -euo pipefail
[ $# -ge 1 ] || { echo "usage: $0 host..." >&2; exit 2; }
python3 - "$@" <<'PY'
import sys, datetime
from cryptography import x509
from cryptography.x509.oid import NameOID, ExtendedKeyUsageOID
from cryptography.hazmat.primitives import hashes, serialization
from cryptography.hazmat.primitives.asymmetric import ec
hosts = sys.argv[1:]
ca = x509.load_pem_x509_certificate(open("ca/ca.pem", "rb").read())
cakey = serialization.load_pem_private_key(open("ca/ca.key", "rb").read(), None)
key = ec.generate_private_key(ec.SECP256R1())
now = datetime.datetime(2020, 1, 1)
cert = (x509.CertificateBuilder()
  .subject_name(x509.Name([x509.NameAttribute(NameOID.COMMON_NAME, hosts[0])]))
  .issuer_name(ca.subject).public_key(key.public_key())
  .serial_number(x509.random_serial_number())
  .not_valid_before(now).not_valid_after(now + datetime.timedelta(days=9000))
  .add_extension(x509.SubjectAlternativeName([x509.DNSName(h) for h in hosts]), False)
  .add_extension(x509.BasicConstraints(ca=False, path_length=None), True)
  .add_extension(x509.ExtendedKeyUsage([ExtendedKeyUsageOID.SERVER_AUTH]), False)
  .add_extension(x509.AuthorityKeyIdentifier.from_issuer_public_key(ca.public_key()), False)
  .sign(cakey, hashes.SHA256()))
open("keys/pokemon-home.key", "wb").write(key.private_bytes(serialization.Encoding.PEM,
  serialization.PrivateFormat.PKCS8, serialization.NoEncryption()))
open("keys/pokemon-home.pem", "wb").write(cert.public_bytes(serialization.Encoding.PEM) + ca.public_bytes(serialization.Encoding.PEM))
print("wrote keys/pokemon-home.pem (leaf + CA) for", ", ".join(hosts))
PY
