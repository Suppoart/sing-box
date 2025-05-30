package wireguard

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/netip"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/ipscanner"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing-box/warp"
	dns "github.com/sagernet/sing-dns"
	E "github.com/sagernet/sing/common/exceptions"
	M "github.com/sagernet/sing/common/metadata"
)

type PeerConfig struct {
	destination    M.Socksaddr
	domainStrategy dns.DomainStrategy
	Endpoint       netip.AddrPort
	PublicKey      string
	PreSharedKey   string
	AllowedIPs     []string
	Reserved       [3]uint8
	TryUnblockWarp bool
}

func (c PeerConfig) GenerateIpcLines() string {
	ipcLines := "\npublic_key=" + c.PublicKey
	ipcLines += "\nendpoint=" + c.Endpoint.String()
	if c.PreSharedKey != "" {
		ipcLines += "\npreshared_key=" + c.PreSharedKey
	}
	for _, allowedIP := range c.AllowedIPs {
		ipcLines += "\nallowed_ip=" + allowedIP
	}
	if c.TryUnblockWarp {
		ipcLines += "\ntry_unblock_warp=true"
	}

	return ipcLines
}

func scanWarpEndpoints(privateKey string, cidrPrefix warp.Prefix, port uint16) (ipscanner.IPInfo, error) {
	ctx, _ := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)

	scanOpts := ipscanner.WarpScanOptions{
		PrivateKey: privateKey,
		PublicKey:  warp.WarpPublicKey,
		MaxRTT:     1000 * time.Millisecond,
		V4:         true,
		V6:         true,
		CidrPrefix: cidrPrefix,
		Port:       port,
	}

	return ipscanner.RunWarpScan(ctx, scanOpts)
}

func ParsePeers(options option.WireGuardOutboundOptions, logger log.ContextLogger) ([]PeerConfig, error) {
	var peers []PeerConfig
	if len(options.Peers) > 0 {
		for peerIndex, rawPeer := range options.Peers {
			peer := PeerConfig{
				AllowedIPs: rawPeer.AllowedIPs,
			}

			if warp.IsPeerCloudflareWarp(rawPeer.PublicKey) {
				switch rawPeer.ServerOptions.Server {
				case "warp_auto":
					logger.Info("running WARP IP scanner, this might take a while...")

					bestEndpoint, err := scanWarpEndpoints(options.PrivateKey, warp.All, rawPeer.ServerOptions.ServerPort)
					if err != nil {
						return nil, err
					}
					logger.Info(fmt.Sprintf("fastest WARP endpoint available is %s with RTT of %s ", bestEndpoint.AddrPort.String(), bestEndpoint.RTT.String()))

					peer.Endpoint = bestEndpoint.AddrPort
					peer.TryUnblockWarp = true
				case "warp_188":
					logger.Info("running WARP IP scanner, this might take a while...")

					bestEndpoint, err := scanWarpEndpoints(options.PrivateKey, warp.Prefix188, rawPeer.ServerOptions.ServerPort)
					if err != nil {
						return nil, err
					}
					logger.Info(fmt.Sprintf("fastest WARP endpoint available is %s with RTT of %s ", bestEndpoint.AddrPort.String(), bestEndpoint.RTT.String()))

					peer.Endpoint = bestEndpoint.AddrPort
					peer.TryUnblockWarp = true
				case "warp_162":
					logger.Info("running WARP IP scanner, this might take a while...")

					bestEndpoint, err := scanWarpEndpoints(options.PrivateKey, warp.Prefix162, rawPeer.ServerOptions.ServerPort)
					if err != nil {
						return nil, err
					}
					logger.Info(fmt.Sprintf("fastest WARP endpoint available is %s with RTT of %s ", bestEndpoint.AddrPort.String(), bestEndpoint.RTT.String()))

					peer.Endpoint = bestEndpoint.AddrPort
					peer.TryUnblockWarp = true
				default:
					destination := rawPeer.ServerOptions.Build()
					if destination.IsFqdn() {
						peer.destination = destination
						peer.domainStrategy = dns.DomainStrategy(options.DomainStrategy)
					} else {
						peer.Endpoint = destination.AddrPort()
					}
				}
			} else {
				destination := rawPeer.ServerOptions.Build()
				if destination.IsFqdn() {
					peer.destination = destination
					peer.domainStrategy = dns.DomainStrategy(options.DomainStrategy)
				} else {
					peer.Endpoint = destination.AddrPort()
				}
			}

			{
				bytes, err := base64.StdEncoding.DecodeString(rawPeer.PublicKey)
				if err != nil {
					return nil, E.Cause(err, "decode public key for peer ", peerIndex)
				}
				peer.PublicKey = hex.EncodeToString(bytes)
			}
			if rawPeer.PreSharedKey != "" {
				bytes, err := base64.StdEncoding.DecodeString(rawPeer.PreSharedKey)
				if err != nil {
					return nil, E.Cause(err, "decode pre shared key for peer ", peerIndex)
				}
				peer.PreSharedKey = hex.EncodeToString(bytes)
			}
			if len(rawPeer.AllowedIPs) == 0 {
				return nil, E.New("missing allowed_ips for peer ", peerIndex)
			}
			if len(rawPeer.Reserved) > 0 {
				if len(rawPeer.Reserved) != 3 {
					return nil, E.New("invalid reserved value for peer ", peerIndex, ", required 3 bytes, got ", len(peer.Reserved))
				}
				copy(peer.Reserved[:], rawPeer.Reserved)
			}

			peers = append(peers, peer)
		}
	} else {
		peer := PeerConfig{}
		var (
			addressHas4 bool
			addressHas6 bool
		)
		for _, localAddress := range options.LocalAddress {
			if localAddress.Addr().Is4() {
				addressHas4 = true
			} else {
				addressHas6 = true
			}
		}
		if addressHas4 {
			peer.AllowedIPs = append(peer.AllowedIPs, netip.PrefixFrom(netip.IPv4Unspecified(), 0).String())
		}
		if addressHas6 {
			peer.AllowedIPs = append(peer.AllowedIPs, netip.PrefixFrom(netip.IPv6Unspecified(), 0).String())
		}

		if warp.IsPeerCloudflareWarp(options.PeerPublicKey) {
			switch options.ServerOptions.Server {
			case "warp_auto":
				logger.Info("running WARP IP scanner, this might take a while...")

				bestEndpoint, err := scanWarpEndpoints(options.PrivateKey, warp.All, options.ServerOptions.ServerPort)
				if err != nil {
					return nil, err
				}
				logger.Info(fmt.Sprintf("fastest WARP endpoint available is %s with RTT of %s ", bestEndpoint.AddrPort.String(), bestEndpoint.RTT.String()))

				peer.Endpoint = bestEndpoint.AddrPort
				peer.TryUnblockWarp = true
			case "warp_188":
				logger.Info("running WARP IP scanner, this might take a while...")

				bestEndpoint, err := scanWarpEndpoints(options.PrivateKey, warp.Prefix188, options.ServerOptions.ServerPort)
				if err != nil {
					return nil, err
				}
				logger.Info(fmt.Sprintf("fastest WARP endpoint available is %s with RTT of %s ", bestEndpoint.AddrPort.String(), bestEndpoint.RTT.String()))

				peer.Endpoint = bestEndpoint.AddrPort
				peer.TryUnblockWarp = true
			case "warp_162":
				logger.Info("running WARP IP scanner, this might take a while...")

				bestEndpoint, err := scanWarpEndpoints(options.PrivateKey, warp.Prefix162, options.ServerOptions.ServerPort)
				if err != nil {
					return nil, err
				}
				logger.Info(fmt.Sprintf("fastest WARP endpoint available is %s with RTT of %s ", bestEndpoint.AddrPort.String(), bestEndpoint.RTT.String()))

				peer.Endpoint = bestEndpoint.AddrPort
				peer.TryUnblockWarp = true
			default:
				destination := options.ServerOptions.Build()
				if destination.IsFqdn() {
					peer.destination = destination
					peer.domainStrategy = dns.DomainStrategy(options.DomainStrategy)
				} else {
					peer.Endpoint = destination.AddrPort()
				}
			}
		} else {
			destination := options.ServerOptions.Build()
			if destination.IsFqdn() {
				peer.destination = destination
				peer.domainStrategy = dns.DomainStrategy(options.DomainStrategy)
			} else {
				peer.Endpoint = destination.AddrPort()
			}
		}

		{
			bytes, err := base64.StdEncoding.DecodeString(options.PeerPublicKey)
			if err != nil {
				return nil, E.Cause(err, "decode peer public key")
			}
			peer.PublicKey = hex.EncodeToString(bytes)
		}
		if options.PreSharedKey != "" {
			bytes, err := base64.StdEncoding.DecodeString(options.PreSharedKey)
			if err != nil {
				return nil, E.Cause(err, "decode pre shared key")
			}
			peer.PreSharedKey = hex.EncodeToString(bytes)
		}
		if len(options.Reserved) > 0 {
			if len(options.Reserved) != 3 {
				return nil, E.New("invalid reserved value, required 3 bytes, got ", len(peer.Reserved))
			}
			copy(peer.Reserved[:], options.Reserved)
		}
		peers = append(peers, peer)
	}
	return peers, nil
}

func ResolvePeers(ctx context.Context, router adapter.Router, peers []PeerConfig) error {
	for peerIndex, peer := range peers {
		if peer.Endpoint.IsValid() {
			continue
		}
		destinationAddresses, err := router.Lookup(ctx, peer.destination.Fqdn, peer.domainStrategy)
		if err != nil {
			if len(peers) == 1 {
				return E.Cause(err, "resolve endpoint domain")
			} else {
				return E.Cause(err, "resolve endpoint domain for peer ", peerIndex)
			}
		}
		if len(destinationAddresses) == 0 {
			return E.New("no addresses found for endpoint domain: ", peer.destination.Fqdn)
		}
		peers[peerIndex].Endpoint = netip.AddrPortFrom(destinationAddresses[0], peer.destination.Port)

	}
	return nil
}
