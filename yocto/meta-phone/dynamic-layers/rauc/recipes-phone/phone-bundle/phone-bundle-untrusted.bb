SUMMARY = "phone-bundle-untrusted: the same update signed by a key the phones do not trust"
DESCRIPTION = "For the update test: installing it must fail on the signature (R37)."

require phone-bundle.bb

PHONE_UPDATE_KEY = "untrusted"
