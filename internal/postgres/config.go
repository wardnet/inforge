package postgres

import (
	"fmt"
	"strings"
)

// ClusterConfig is the input to the config + realization renderers for one
// database-cluster on its host.
type ClusterConfig struct {
	Cluster     string // the cluster resource name
	Version     string // pinned Postgres major ("" → DefaultVersion)
	ListenIP    string // the host's private IP; peers dial this (localhost is always added)
	Port        int    // the cluster's TCP port (postgres.ClusterPort)
	NetworkCIDR string // the private-network CIDR admitted in pg_hba (scram-sha-256); never public
}

func (c ClusterConfig) validate() error {
	switch {
	case c.Cluster == "":
		return fmt.Errorf("postgres: empty cluster name")
	case c.ListenIP == "":
		return fmt.Errorf("postgres: empty listen IP for cluster %q", c.Cluster)
	case c.Port <= 0:
		return fmt.Errorf("postgres: invalid port %d for cluster %q", c.Port, c.Cluster)
	case c.NetworkCIDR == "":
		return fmt.Errorf("postgres: empty network CIDR for cluster %q", c.Cluster)
	}
	return nil
}

// RenderPostgresqlConf renders the cluster's postgresql.conf. The postmaster listens
// on localhost plus the host's private IP only (never 0.0.0.0) — peer services reach
// it over the private network, and the firewall additionally restricts 5432 to the
// private CIDR. Passwords are stored scram-sha-256.
func RenderPostgresqlConf(c ClusterConfig) (string, error) {
	if err := c.validate(); err != nil {
		return "", err
	}
	lines := []string{
		"# Managed by inforge (ADR-0036) — do not edit by hand.",
		fmt.Sprintf("listen_addresses = 'localhost,%s'", c.ListenIP),
		fmt.Sprintf("port = %d", c.Port),
		fmt.Sprintf("unix_socket_directories = '%s'", SocketDir),
		fmt.Sprintf("data_directory = '%s'", DataDir(c.Cluster)),
		"password_encryption = scram-sha-256",
	}
	return strings.Join(lines, "\n") + "\n", nil
}

// RenderHBA renders the cluster's pg_hba.conf. Local connections (superuser
// maintenance, role minting, backups) use peer auth over the unix socket — no
// password. Per-service roles connect over TCP from the private network only, with
// scram-sha-256. There is deliberately NO rule for 0.0.0.0/0 — the cluster is never
// reachable from the public internet.
func RenderHBA(c ClusterConfig) (string, error) {
	if err := c.validate(); err != nil {
		return "", err
	}
	lines := []string{
		"# Managed by inforge (ADR-0036) — do not edit by hand.",
		"# TYPE  DATABASE  USER  ADDRESS            METHOD",
		"local   all       all                      peer",
		fmt.Sprintf("host    all       all   %s   scram-sha-256", c.NetworkCIDR),
		"host    all       all   127.0.0.1/32       scram-sha-256",
		"host    all       all   ::1/128            scram-sha-256",
	}
	return strings.Join(lines, "\n") + "\n", nil
}
