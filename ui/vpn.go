package ui

// VPNState is vpn.list: the profiles, one of which at most is up, and the
// public half of the key pair vpn.keygen made that no profile uses yet.
// Backend: "networkd" (WireGuard through systemd-networkd), "sim" (a
// developer machine) or "" (no VPN on this phone). AlwaysOn is the profile
// kept connected (reconnected after a network change, a failure or a
// restart), KillSwitch blocks connections without it, and Blocking says
// the block holds now (the always-on VPN is not connected). Apps is which
// apps go through the tunnel (VPNAppsAll, VPNAppsOnly, VPNAppsExcept),
// AppList the apps that use the network, each Chosen or not.
type VPNState struct {
	Backend    string       `json:"backend"`
	Profiles   []VPNProfile `json:"profiles"`
	PendingKey string       `json:"pending_key,omitempty"`
	AlwaysOn   string       `json:"always_on,omitempty"`
	KillSwitch bool         `json:"kill_switch,omitempty"`
	Blocking   bool         `json:"blocking,omitempty"`
	Apps       string       `json:"apps,omitempty"`
	AppList    []VPNApp     `json:"app_list"`
}

// A profile's State.
const (
	VPNConnecting    = "connecting"
	VPNConnected     = "connected"
	VPNDisconnecting = "disconnecting"
)

// Which apps the tunnel takes: every app and the system (""), only the
// chosen apps, or all but the chosen apps.
const (
	VPNAppsAll    = ""
	VPNAppsOnly   = "only"
	VPNAppsExcept = "except"
)

// VPNProfile is a profile as Settings sees it: no private or preshared
// key, only the phone's public key (for the server's administrator).
type VPNProfile struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	State string `json:"state,omitempty"`
	// Error: why its last connection failed or ended.
	Error      string   `json:"error,omitempty"`
	Address    []string `json:"address"`
	DNS        []string `json:"dns,omitempty"`
	Endpoint   string   `json:"endpoint"` // the first peer's
	Peers      int      `json:"peers"`
	FullTunnel bool     `json:"full_tunnel,omitempty"`
	AllowedIPs []string `json:"allowed_ips"`
	PublicKey  string   `json:"public_key"`
	// While connected: since when (unix seconds), the latest handshake
	// with a peer (0: none yet) and the bytes through the tunnel.
	Since     int64  `json:"since,omitempty"`
	Handshake int64  `json:"handshake,omitempty"`
	RxBytes   uint64 `json:"rx_bytes,omitempty"`
	TxBytes   uint64 `json:"tx_bytes,omitempty"`
}

// VPNApp is an app Settings › VPN › Apps lists.
type VPNApp struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Chosen bool   `json:"chosen,omitempty"`
}

// VPNArgs: vpn.connect, vpn.disconnect and vpn.delete take ID; vpn.import
// takes nothing (the user picks the file in the system's list) and
// answers the profile; vpn.keygen answers {public_key}. vpn.set takes
// AlwaysOn with ID (true: that profile, connected now and kept so; false:
// none), KillSwitch, Apps (the mode) or App and Chosen (one app in or out
// of the list), and answers vpn.list's state.
type VPNArgs struct {
	ID         string  `json:"id,omitempty"`
	AlwaysOn   *bool   `json:"always_on,omitempty"`
	KillSwitch *bool   `json:"kill_switch,omitempty"`
	Apps       *string `json:"apps,omitempty"`
	App        string  `json:"app,omitempty"`
	Chosen     *bool   `json:"chosen,omitempty"`
}
