// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of go-ra

package ra

import (
	"encoding/binary"
	"net/netip"
	"testing"
	"time"

	"github.com/go-playground/validator/v10"
	"github.com/mdlayher/ndp"
	"github.com/stretchr/testify/require"
)

// raOptionBytes returns the wire format of the options of an RA that carries
// only opt.
func raOptionBytes(t *testing.T, opt ndp.Option) []byte {
	t.Helper()
	b, err := ndp.MarshalMessage(&ndp.RouterAdvertisement{Options: []ndp.Option{opt}})
	require.NoError(t, err)
	// 4 bytes of ICMPv6 header and 12 bytes of RA header precede the options.
	return b[16:]
}

func TestRouteOptionLength(t *testing.T) {
	tests := []struct {
		name         string
		prefix       string
		optionLength int
		wantBytes    int
	}{
		{"default route, shortest", "::/0", 0, 8},
		{"default route, 16 bytes", "::/0", 2, 16},
		{"default route, 24 bytes", "::/0", 3, 24},
		{"/48, shortest", "2001:db8:1::/48", 0, 16},
		{"/48, explicit shortest", "2001:db8:1::/48", 2, 16},
		{"/48, 24 bytes", "2001:db8:1::/48", 3, 24},
		{"/64, 24 bytes", "2001:db8:1:2::/64", 3, 24},
		{"/96, shortest", "2001:db8:1:2:3:4::/96", 0, 24},
		{"/96, explicit", "2001:db8:1:2:3:4::/96", 3, 24},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := netip.MustParsePrefix(tt.prefix)
			b := raOptionBytes(t, routeOption(p, ndp.High, 1800*time.Second, tt.optionLength))

			require.Len(t, b, tt.wantBytes)
			require.Equal(t, byte(24), b[0], "option type")
			require.Equal(t, byte(tt.wantBytes/8), b[1], "Length field")
			require.Equal(t, byte(p.Bits()), b[2], "prefix length")
			require.Equal(t, byte(ndp.High)<<3, b[3], "preference")
			require.Equal(t, uint32(1800), binary.BigEndian.Uint32(b[4:8]), "route lifetime")

			// The prefix field holds the leading octets of the prefix and is
			// zero-padded.
			want := make([]byte, tt.wantBytes-8)
			addr := p.Addr().As16()
			copy(want, addr[:])
			require.Equal(t, want, b[8:])
		})
	}
}

func TestRouteOptionLengthMatchesLibraryEncoding(t *testing.T) {
	// With the default length, the option must be byte-identical to what
	// the daemon sent before the length became configurable.
	for _, prefix := range []string{"::/0", "2001:db8::/32", "2001:db8:1:2::/64", "2001:db8:1:2:3::/80"} {
		p := netip.MustParsePrefix(prefix)
		want := raOptionBytes(t, &ndp.RouteInformation{
			PrefixLength:  uint8(p.Bits()),
			Preference:    ndp.Medium,
			RouteLifetime: 600 * time.Second,
			Prefix:        p.Addr(),
		})
		require.Equal(t, want, raOptionBytes(t, routeOption(p, ndp.Medium, 600*time.Second, 0)), prefix)
	}
}

func TestZeroOptionLifetimesRouteOptionWithLength(t *testing.T) {
	opt := routeOption(netip.MustParsePrefix("2001:db8:1::/48"), ndp.Medium, 1800*time.Second, 3)
	before := raOptionBytes(t, opt)

	zeroed := raOptionBytes(t, zeroOptionLifetimes([]ndp.Option{opt})[0])

	require.Equal(t, uint32(0), binary.BigEndian.Uint32(zeroed[4:8]), "route lifetime is zeroed")
	require.Equal(t, before[:4], zeroed[:4])
	require.Equal(t, before[8:], zeroed[8:])
	require.Equal(t, before, raOptionBytes(t, opt), "the original option is not modified")
}

func TestRouteOptionLengthValidation(t *testing.T) {
	tests := []struct {
		name         string
		prefix       string
		optionLength int
		errorTag     string
	}{
		{"default selects the shortest", "2001:db8::/64", 0, ""},
		{"longer than needed", "2001:db8::/64", 3, ""},
		{"default route in 8 bytes", "::/0", 1, ""},
		{"too short for a /64", "2001:db8::/64", 1, "route_option_length"},
		{"too short for a /96", "2001:db8:1:2:3:4::/96", 2, "route_option_length"},
		{"above the maximum", "2001:db8::/64", 4, "lte"},
		{"negative", "2001:db8::/64", -1, "gte"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &Config{Interfaces: []*InterfaceConfig{{
				Name:                   "net0",
				RAIntervalMilliseconds: 1000,
				Routes: []*RouteConfig{{
					Prefix:          tt.prefix,
					LifetimeSeconds: 100,
					OptionLength:    tt.optionLength,
				}},
			}}}

			err := c.defaultAndValidate()
			if tt.errorTag == "" {
				require.NoError(t, err)
				return
			}
			var verr validator.ValidationErrors
			require.ErrorAs(t, err, &verr)
			for _, v := range verr {
				if v.Field() == "OptionLength" && v.Tag() == tt.errorTag {
					return
				}
			}
			require.Failf(t, "expected error not found", verr.Error())
		})
	}
}
