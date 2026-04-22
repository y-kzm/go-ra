// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of go-ra

package internal

import (
	ra "github.com/YutaroHayakawa/go-ra"
	gorav1 "github.com/YutaroHayakawa/go-ra/api/gora/v1"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

func InterfaceConfigToProto(c *ra.InterfaceConfig) *gorav1.InterfaceConfig {
	p := &gorav1.InterfaceConfig{
		Id:                        int32(c.ID),
		Description:               c.Description,
		Name:                      c.Name,
		RaIntervalMilliseconds:    int32(c.RAIntervalMilliseconds),
		CurrentHopLimit:           int32(c.CurrentHopLimit),
		Managed:                   c.Managed,
		Other:                     c.Other,
		Preference:                c.Preference,
		RouterLifetimeSeconds:     int32(c.RouterLifetimeSeconds),
		ReachableTimeMilliseconds: int64(c.ReachableTimeMilliseconds),
		RetransmitTimeMilliseconds: int64(c.RetransmitTimeMilliseconds),
		Mtu:                       int64(c.MTU),
		Clients:                   c.Clients,
		DisableRsReply:            c.DisableRSReply,
	}

	if c.SendGoodbye != nil {
		p.SendGoodbye = wrapperspb.Bool(*c.SendGoodbye)
	}

	for _, prefix := range c.Prefixes {
		pp := &gorav1.PrefixConfig{
			Prefix:    prefix.Prefix,
			OnLink:    prefix.OnLink,
			Autonomous: prefix.Autonomous,
		}
		if prefix.ValidLifetimeSeconds != nil {
			pp.ValidLifetimeSeconds = wrapperspb.Int64(int64(*prefix.ValidLifetimeSeconds))
		}
		if prefix.PreferredLifetimeSeconds != nil {
			pp.PreferredLifetimeSeconds = wrapperspb.Int64(int64(*prefix.PreferredLifetimeSeconds))
		}
		p.Prefixes = append(p.Prefixes, pp)
	}

	for _, route := range c.Routes {
		p.Routes = append(p.Routes, &gorav1.RouteConfig{
			Prefix:          route.Prefix,
			LifetimeSeconds: int64(route.LifetimeSeconds),
			Preference:      route.Preference,
		})
	}

	for _, rdnss := range c.RDNSSes {
		p.Rdnsses = append(p.Rdnsses, &gorav1.RdnssConfig{
			LifetimeSeconds: int64(rdnss.LifetimeSeconds),
			Addresses:       rdnss.Addresses,
		})
	}

	for _, dnssl := range c.DNSSLs {
		p.Dnssls = append(p.Dnssls, &gorav1.DnsslConfig{
			LifetimeSeconds: int64(dnssl.LifetimeSeconds),
			DomainNames:     dnssl.DomainNames,
		})
	}

	for _, nat64 := range c.NAT64Prefixes {
		pnat64 := &gorav1.Nat64PrefixConfig{
			Prefix: nat64.Prefix,
		}
		if nat64.LifetimeSeconds != nil {
			pnat64.LifetimeSeconds = wrapperspb.Int64(int64(*nat64.LifetimeSeconds))
		}
		p.Nat64Prefixes = append(p.Nat64Prefixes, pnat64)
	}

	return p
}

func InterfaceConfigFromProto(p *gorav1.InterfaceConfig) *ra.InterfaceConfig {
	c := &ra.InterfaceConfig{
		ID:                         int(p.Id),
		Description:                p.Description,
		Name:                       p.Name,
		RAIntervalMilliseconds:     int(p.RaIntervalMilliseconds),
		CurrentHopLimit:            int(p.CurrentHopLimit),
		Managed:                    p.Managed,
		Other:                      p.Other,
		Preference:                 p.Preference,
		RouterLifetimeSeconds:      int(p.RouterLifetimeSeconds),
		ReachableTimeMilliseconds:  int(p.ReachableTimeMilliseconds),
		RetransmitTimeMilliseconds: int(p.RetransmitTimeMilliseconds),
		MTU:                        int(p.Mtu),
		Clients:                    p.Clients,
		DisableRSReply:             p.DisableRsReply,
	}

	if p.SendGoodbye != nil {
		v := p.SendGoodbye.Value
		c.SendGoodbye = &v
	}

	for _, pp := range p.Prefixes {
		prefix := &ra.PrefixConfig{
			Prefix:    pp.Prefix,
			OnLink:    pp.OnLink,
			Autonomous: pp.Autonomous,
		}
		if pp.ValidLifetimeSeconds != nil {
			v := int(pp.ValidLifetimeSeconds.Value)
			prefix.ValidLifetimeSeconds = &v
		}
		if pp.PreferredLifetimeSeconds != nil {
			v := int(pp.PreferredLifetimeSeconds.Value)
			prefix.PreferredLifetimeSeconds = &v
		}
		c.Prefixes = append(c.Prefixes, prefix)
	}

	for _, pr := range p.Routes {
		c.Routes = append(c.Routes, &ra.RouteConfig{
			Prefix:          pr.Prefix,
			LifetimeSeconds: int(pr.LifetimeSeconds),
			Preference:      pr.Preference,
		})
	}

	for _, pr := range p.Rdnsses {
		c.RDNSSes = append(c.RDNSSes, &ra.RDNSSConfig{
			LifetimeSeconds: int(pr.LifetimeSeconds),
			Addresses:       pr.Addresses,
		})
	}

	for _, pd := range p.Dnssls {
		c.DNSSLs = append(c.DNSSLs, &ra.DNSSLConfig{
			LifetimeSeconds: int(pd.LifetimeSeconds),
			DomainNames:     pd.DomainNames,
		})
	}

	for _, pn := range p.Nat64Prefixes {
		nat64 := &ra.NAT64PrefixConfig{
			Prefix: pn.Prefix,
		}
		if pn.LifetimeSeconds != nil {
			v := int(pn.LifetimeSeconds.Value)
			nat64.LifetimeSeconds = &v
		}
		c.NAT64Prefixes = append(c.NAT64Prefixes, nat64)
	}

	return c
}
