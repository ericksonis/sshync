package sshconf

import "strings"

// Client keywords from ssh_config(5), OpenSSH 9.8.
var keywords = []string{
	"Host", "Match", "AddKeysToAgent", "AddressFamily", "BatchMode", "BindAddress",
	"BindInterface", "CanonicalDomains", "CanonicalizeFallbackLocal", "CanonicalizeHostname",
	"CanonicalizeMaxDots", "CanonicalizePermittedCNAMEs", "CASignatureAlgorithms",
	"CertificateFile", "ChannelTimeout", "CheckHostIP", "Ciphers", "ClearAllForwardings",
	"Compression", "ConnectionAttempts", "ConnectTimeout", "ControlMaster", "ControlPath",
	"ControlPersist", "DynamicForward", "EnableEscapeCommandline", "EnableSSHKeysign",
	"EscapeChar", "ExitOnForwardFailure", "FingerprintHash", "ForkAfterAuthentication",
	"ForwardAgent", "ForwardX11", "ForwardX11Timeout", "ForwardX11Trusted", "GatewayPorts",
	"GlobalKnownHostsFile", "GSSAPIAuthentication", "GSSAPIDelegateCredentials",
	"HashKnownHosts", "HostbasedAcceptedAlgorithms", "HostbasedAuthentication",
	"HostKeyAlgorithms", "HostKeyAlias", "HostName", "IdentitiesOnly", "IdentityAgent",
	"IdentityFile", "IgnoreUnknown", "Include", "IPQoS", "KbdInteractiveAuthentication",
	"KbdInteractiveDevices", "KexAlgorithms", "KnownHostsCommand", "LocalCommand",
	"LocalForward", "LogLevel", "LogVerbose", "MACs", "NoHostAuthenticationForLocalhost",
	"NumberOfPasswordPrompts", "ObscureKeystrokeTiming", "PasswordAuthentication",
	"PermitLocalCommand", "PermitRemoteOpen", "PKCS11Provider", "Port",
	"PreferredAuthentications", "ProxyCommand", "ProxyJump", "ProxyUseFdpass",
	"PubkeyAcceptedAlgorithms", "PubkeyAuthentication", "RekeyLimit", "RemoteCommand",
	"RemoteForward", "RequestTTY", "RequiredRSASize", "RevokedHostKeys",
	"SecurityKeyProvider", "SendEnv", "ServerAliveCountMax", "ServerAliveInterval",
	"SessionType", "SetEnv", "StdinNull", "StreamLocalBindMask", "StreamLocalBindUnlink",
	"StrictHostKeyChecking", "SyslogFacility", "TCPKeepAlive", "Tag", "Tunnel",
	"TunnelDevice", "UpdateHostKeys", "User", "UserKnownHostsFile", "VerifyHostKeyDNS",
	"VisualHostKey", "XAuthLocation",
	// deprecated aliases still accepted by ssh
	"PubkeyAcceptedKeyTypes", "HostbasedKeyTypes", "ChallengeResponseAuthentication",
}

var canonical = func() map[string]string {
	m := make(map[string]string, len(keywords))
	for _, k := range keywords {
		m[strings.ToLower(k)] = k
	}
	return m
}()

// CanonicalKey returns the documented spelling of key, or key unchanged.
func CanonicalKey(key string) string {
	if c, ok := canonical[strings.ToLower(key)]; ok {
		return c
	}
	return key
}

// KnownKey reports whether key is a recognised ssh_config keyword.
func KnownKey(key string) bool {
	_, ok := canonical[strings.ToLower(key)]
	return ok
}

var repeatable = map[string]bool{
	"identityfile": true, "certificatefile": true, "localforward": true,
	"remoteforward": true, "dynamicforward": true, "sendenv": true, "setenv": true,
}

// Repeatable reports whether ssh accepts the keyword multiple times, with
// every occurrence taking effect.
func Repeatable(key string) bool { return repeatable[strings.ToLower(key)] }

// Keywords returns the settable client keywords (no Host/Match, no deprecated aliases).
func Keywords() []string {
	var out []string
	for _, k := range keywords {
		switch k {
		case "Host", "Match", "PubkeyAcceptedKeyTypes", "HostbasedKeyTypes", "ChallengeResponseAuthentication":
			continue
		}
		out = append(out, k)
	}
	return out
}

// YesNo lists commonly toggled keywords whose value is yes or no.
var YesNo = []string{
	"IdentitiesOnly", "ForwardAgent", "ForwardX11", "ForwardX11Trusted", "Compression",
	"BatchMode", "CheckHostIP", "ClearAllForwardings", "ExitOnForwardFailure",
	"GatewayPorts", "GSSAPIAuthentication", "HashKnownHosts", "PasswordAuthentication",
	"PubkeyAuthentication", "TCPKeepAlive", "VisualHostKey", "KbdInteractiveAuthentication",
}
