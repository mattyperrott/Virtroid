#!/usr/bin/env python3
"""Checks that a build is signed with Virtroid's keys and nothing else.

check-signatures.py SRC PRODUCT_OUT KEYS PREBUILT_SHA256

Every APK and APEX installed in PRODUCT_OUT, and every APK inside each APEX,
must be signed with a certificate in KEYS (*.x509.pem), and every APEX's
payload with a key in KEYS (*.avbpubkey). The one exception is an APK
byte-identical to one PREBUILT_SHA256 pins, which keeps its publisher's
signature. A key the build can't find in our directory falls back silently to
the module's own, public, test key, so this is what proves none did.
Prints each file that fails, with its signer, and exits 1 if any does.
"""
import base64
import glob
import hashlib
import io
import os
import subprocess
import sys
import tempfile
import zipfile

src, product_out, keys, prebuilt_sha256 = sys.argv[1:5]
host = os.path.join(src, "out/host/linux-x86/bin")
java = os.path.join(src, "prebuilts/jdk/jdk21/linux-x86/bin/java")
apksigner = os.path.join(src, "prebuilts/sdk/tools/linux/lib/apksigner.jar")


def cert_digest(path):
    pem = open(path).read()
    der = base64.b64decode("".join(pem.split("-----")[2].split()))
    return hashlib.sha256(der).hexdigest()


def sha256(data):
    return hashlib.sha256(data).hexdigest()


our_certs = {cert_digest(p) for p in glob.glob(os.path.join(keys, "*.x509.pem"))}
our_payload_keys = {sha256(open(p, "rb").read()) for p in glob.glob(os.path.join(keys, "*.avbpubkey"))}
pinned = set()
for line in open(prebuilt_sha256):
    if line.strip() and not line.lstrip().startswith("#"):
        pinned.add(line.split()[0])
if not our_certs or not our_payload_keys:
    sys.exit("no certificates or payload keys in %s" % keys)

failures = []
checked = 0


def signers(path):
    r = subprocess.run([java, "-jar", apksigner, "verify", "--print-certs", path],
                       capture_output=True, text=True)
    if r.returncode != 0:
        return None
    return [l.split(": ", 1)[1].strip() for l in r.stdout.splitlines()
            if "certificate SHA-256 digest" in l]


def check_apk(path, label):
    global checked
    checked += 1
    if sha256(open(path, "rb").read()) in pinned:
        return
    digests = signers(path)
    if not digests:
        failures.append("%s: not signed" % label)
    elif not set(digests) <= our_certs:
        failures.append("%s: signed by %s" % (label, ", ".join(d[:16] for d in digests)))


def check_apex(path, label):
    check_apk(path, label)
    apex = path
    with zipfile.ZipFile(path) as z:
        if path.endswith(".capex"):
            apex_bytes = z.read("original_apex")
            payload_key = zipfile.ZipFile(io.BytesIO(apex_bytes)).read("apex_pubkey")
        else:
            apex_bytes = None
            payload_key = z.read("apex_pubkey")
    if sha256(payload_key) not in our_payload_keys:
        failures.append("%s: payload key %s" % (label, sha256(payload_key)[:16]))
    with tempfile.TemporaryDirectory() as tmp:
        if apex_bytes is not None:
            apex = os.path.join(tmp, "original.apex")
            open(apex, "wb").write(apex_bytes)
        out = os.path.join(tmp, "payload")
        r = subprocess.run([os.path.join(host, "deapexer"),
                            "--debugfs_path", os.path.join(host, "debugfs_static"),
                            "--fsckerofs_path", os.path.join(host, "fsck.erofs"),
                            "extract", apex, out], capture_output=True, text=True)
        if r.returncode != 0:
            failures.append("%s: can't extract (%s)" % (label, r.stderr.strip()[-100:]))
            return
        for d, _, files in os.walk(out):
            for f in files:
                if f.endswith(".apk"):
                    check_apk(os.path.join(d, f), "%s!%s" % (label, os.path.relpath(os.path.join(d, f), out)))


for top in ("system", "system_ext", "product", "vendor", "odm"):
    for d, _, files in os.walk(os.path.join(product_out, top)):
        for f in sorted(files):
            path = os.path.join(d, f)
            label = os.path.relpath(path, product_out)
            if f.endswith((".apex", ".capex")):
                check_apex(path, label)
            elif f.endswith(".apk"):
                check_apk(path, label)

for f in failures:
    print("not ours: " + f)
print("checked %d APKs and APEXes: %d not signed with our keys" % (checked, len(failures)))
sys.exit(1 if failures else 0)
