package service

import wgutil "github.com/mhsanaei/3x-ui/v3/internal/util/wireguard"

// A real X25519 pair, so PublicKeyFromPrivate agrees with the stored value.
var awgTestPrivateKey, awgTestPublicKey = func() (string, string) {
	priv, pub, err := wgutil.GenerateWireguardKeypair()
	if err != nil {
		panic(err)
	}
	return priv, pub
}()
