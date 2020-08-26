package v1alpha1

import (
	"testing"

	capi "github.com/hashicorp/consul/api"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestToConsul(t *testing.T) {
	cases := map[string]struct {
		input    *ServiceDefaults
		expected *capi.ServiceConfigEntry
	}{
		"kind:service-defaults": {
			&ServiceDefaults{},
			&capi.ServiceConfigEntry{
				Kind: capi.ServiceDefaults,
			},
		},
		"name:name": {
			&ServiceDefaults{
				ObjectMeta: metav1.ObjectMeta{
					Name: "name",
				},
			},
			&capi.ServiceConfigEntry{
				Kind: capi.ServiceDefaults,
				Name: "name",
			},
		},
		"protocol:http": {
			&ServiceDefaults{
				Spec: ServiceDefaultsSpec{
					Protocol: "http",
				},
			},
			&capi.ServiceConfigEntry{
				Kind:     capi.ServiceDefaults,
				Protocol: "http",
			},
		},
		"protocol:''": {
			&ServiceDefaults{
				Spec: ServiceDefaultsSpec{
					Protocol: "",
				},
			},
			&capi.ServiceConfigEntry{
				Kind:     capi.ServiceDefaults,
				Protocol: "",
			},
		},
		"meshgateway.mode:local": {
			&ServiceDefaults{
				Spec: ServiceDefaultsSpec{
					MeshGateway: MeshGatewayConfig{Mode: "local"},
				},
			},
			&capi.ServiceConfigEntry{
				Kind:        capi.ServiceDefaults,
				MeshGateway: capi.MeshGatewayConfig{Mode: capi.MeshGatewayModeLocal},
			},
		},
		"meshgateway.mode:remote": {
			&ServiceDefaults{
				Spec: ServiceDefaultsSpec{
					MeshGateway: MeshGatewayConfig{Mode: "remote"},
				},
			},
			&capi.ServiceConfigEntry{
				Kind:        capi.ServiceDefaults,
				MeshGateway: capi.MeshGatewayConfig{Mode: capi.MeshGatewayModeRemote},
			},
		},
		"meshgateway.mode:none": {
			&ServiceDefaults{
				Spec: ServiceDefaultsSpec{
					MeshGateway: MeshGatewayConfig{Mode: "none"},
				},
			},
			&capi.ServiceConfigEntry{
				Kind:        capi.ServiceDefaults,
				MeshGateway: capi.MeshGatewayConfig{Mode: capi.MeshGatewayModeNone},
			},
		},
		"meshgateway.mode:unsupported": {
			&ServiceDefaults{
				Spec: ServiceDefaultsSpec{
					MeshGateway: MeshGatewayConfig{Mode: "unsupported"},
				},
			},
			&capi.ServiceConfigEntry{
				Kind:        capi.ServiceDefaults,
				MeshGateway: capi.MeshGatewayConfig{Mode: capi.MeshGatewayModeDefault},
			},
		},
		"meshgateway.mode:''": {
			&ServiceDefaults{
				Spec: ServiceDefaultsSpec{
					MeshGateway: MeshGatewayConfig{Mode: ""},
				},
			},
			&capi.ServiceConfigEntry{
				Kind:        capi.ServiceDefaults,
				MeshGateway: capi.MeshGatewayConfig{Mode: capi.MeshGatewayModeDefault},
			},
		},
		"expose:empty,checks:true": {
			&ServiceDefaults{
				Spec: ServiceDefaultsSpec{
					Expose: ExposeConfig{
						Checks: true,
						Paths: []ExposePath{},
					},
				},
			},
			&capi.ServiceConfigEntry{
				Kind: capi.ServiceDefaults,
				Expose: capi.ExposeConfig{
					Checks: true,
					Paths: nil,
				},
			},
		},
		"expose:empty,checks:false": {
			&ServiceDefaults{
				Spec: ServiceDefaultsSpec{
					Expose: ExposeConfig{
						Checks: false,
						Paths: []ExposePath{},
					},
				},
			},
			&capi.ServiceConfigEntry{
				Kind: capi.ServiceDefaults,
				Expose: capi.ExposeConfig{
					Checks: false,
					Paths: nil,
				},
			},
		},
		"expose:single path": {
			&ServiceDefaults{
				Spec: ServiceDefaultsSpec{
					Expose: ExposeConfig{
						Paths: []ExposePath{
							{
								ListenerPort:  80,
								Path:          "/test/path",
								LocalPathPort: 42,
								Protocol:      "tcp",
							},
						},
					},
				},
			},
			&capi.ServiceConfigEntry{
				Kind:      capi.ServiceDefaults,
				Expose: capi.ExposeConfig{
					Paths: []capi.ExposePath{
						{
							ListenerPort:  80,
							Path:          "/test/path",
							LocalPathPort: 42,
							Protocol:      "tcp",
						},
					},
				},
			},
		},
		"expose:multiple paths": {
			&ServiceDefaults{
				Spec: ServiceDefaultsSpec{
					Expose: ExposeConfig{
						Paths: []ExposePath{
							{
								ListenerPort:  80,
								Path:          "/test/path",
								LocalPathPort: 42,
								Protocol:      "tcp",
							},
							{
								ListenerPort:  90,
								Path:          "/test/path2",
								LocalPathPort: 52,
								Protocol:      "http",
							},
						},
					},
				},
			},
			&capi.ServiceConfigEntry{
				Kind:      capi.ServiceDefaults,
				Expose: capi.ExposeConfig{
					Paths: []capi.ExposePath{
						{
							ListenerPort:  80,
							Path:          "/test/path",
							LocalPathPort: 42,
							Protocol:      "tcp",
						},
						{
							ListenerPort:  90,
							Path:          "/test/path2",
							LocalPathPort: 52,
							Protocol:      "http",
						},
					},
				},
			},
		},
		"externalsni:''": {
			&ServiceDefaults{
				Spec: ServiceDefaultsSpec{
					ExternalSNI: "",
				},
			},
			&capi.ServiceConfigEntry{
				Kind:      capi.ServiceDefaults,
				ExternalSNI: "",
			},
		},
		"externalsni:sni": {
			&ServiceDefaults{
				Spec: ServiceDefaultsSpec{
					ExternalSNI: "sni",
				},
			},
			&capi.ServiceConfigEntry{
				Kind:      capi.ServiceDefaults,
				ExternalSNI: "sni",
			},
		},
	}

	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			output := testCase.input.ToConsul()
			require.Equal(t, testCase.expected, output)
		})
	}
}

func TestMatchesConsul(t *testing.T) {
	cases := map[string]struct {
		internal *ServiceDefaults
		consul   *capi.ServiceConfigEntry
		matches  bool
	}{
		"name:matches": {
			&ServiceDefaults{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "my-test-service",
				},
			},
			&capi.ServiceConfigEntry{
				Kind:      capi.ServiceDefaults,
				Name:      "my-test-service",
			},
			true,
		},
		"name:mismatched": {
			&ServiceDefaults{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "my-test-service",
				},
			},
			&capi.ServiceConfigEntry{
				Kind:      capi.ServiceDefaults,
				Name:      "differently-named-service",
			},
			false,
		},
		"protocol:matches": {
			&ServiceDefaults{
				Spec: ServiceDefaultsSpec{
					Protocol: "http",
				},
			},
			&capi.ServiceConfigEntry{
				Kind:      capi.ServiceDefaults,
				Protocol:  "http",
			},
			true,
		},
		"protocol:mismatched": {
			&ServiceDefaults{
				Spec: ServiceDefaultsSpec{
					Protocol: "http",
				},
			},
			&capi.ServiceConfigEntry{
				Protocol:  "https",
			},
			false,
		},
		"gatewayConfig:matches": {
			&ServiceDefaults{
				Spec: ServiceDefaultsSpec{
					MeshGateway: MeshGatewayConfig{
						Mode: "remote",
					},
				},
			},
			&capi.ServiceConfigEntry{
				MeshGateway: capi.MeshGatewayConfig{
					Mode: capi.MeshGatewayModeRemote,
				},
			},
			true,
		},
		"gatewayConfig:mismatched": {
			&ServiceDefaults{
				Spec: ServiceDefaultsSpec{
					MeshGateway: MeshGatewayConfig{
						Mode: "remote",
					},
				},
			},
			&capi.ServiceConfigEntry{
				MeshGateway: capi.MeshGatewayConfig{
					Mode: capi.MeshGatewayModeLocal,
				},
			},
			false,
		},
		"externalSNI:matches": {
			&ServiceDefaults{
				Spec: ServiceDefaultsSpec{
					ExternalSNI: "test-external-sni",
				},
			},
			&capi.ServiceConfigEntry{
				ExternalSNI: "test-external-sni",
			},
			true,
		},
		"externalSNI:mismatched": {
			&ServiceDefaults{
				Spec: ServiceDefaultsSpec{
					ExternalSNI: "test-external-sni",
				},
			},
			&capi.ServiceConfigEntry{
				Kind:      capi.ServiceDefaults,
				ExternalSNI: "different-external-sni",
			},
			false,
		},
		"expose.checks:matches": {
			&ServiceDefaults{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "my-test-service",
					Namespace: "consul-config",
				},
				Spec: ServiceDefaultsSpec{
					Protocol: "https",
					MeshGateway: MeshGatewayConfig{
						Mode: "local",
					},
					Expose: ExposeConfig{
						Checks: true,
						Paths: []ExposePath{
							{
								ListenerPort:  80,
								Path:          "/test/path",
								LocalPathPort: 42,
								Protocol:      "tcp",
							},
						},
					},
					ExternalSNI: "test-external-sni",
				},
			},
			&capi.ServiceConfigEntry{
				Kind:      capi.ServiceDefaults,
				Name:      "my-test-service",
				Namespace: "",
				Protocol:  "https",
				MeshGateway: capi.MeshGatewayConfig{
					Mode: capi.MeshGatewayModeLocal,
				},
				Expose: capi.ExposeConfig{
					Checks: true,
					Paths: []capi.ExposePath{
						{
							ListenerPort:  80,
							Path:          "/test/path",
							LocalPathPort: 42,
							Protocol:      "tcp",
						},
					},
				},
				ExternalSNI: "test-external-sni",
			},
			true,
		},
		"expose.checks:mismatched": {
			&ServiceDefaults{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "my-test-service",
					Namespace: "consul-config",
				},
				Spec: ServiceDefaultsSpec{
					Protocol: "https",
					MeshGateway: MeshGatewayConfig{
						Mode: "local",
					},
					Expose: ExposeConfig{
						Checks: true,
						Paths: []ExposePath{
							{
								ListenerPort:  80,
								Path:          "/test/path",
								LocalPathPort: 42,
								Protocol:      "tcp",
							},
						},
					},
					ExternalSNI: "test-external-sni",
				},
			},
			&capi.ServiceConfigEntry{
				Kind:      capi.ServiceDefaults,
				Name:      "my-test-service",
				Namespace: "",
				Protocol:  "https",
				MeshGateway: capi.MeshGatewayConfig{
					Mode: capi.MeshGatewayModeLocal,
				},
				Expose: capi.ExposeConfig{
					Checks: false,
					Paths: []capi.ExposePath{
						{
							ListenerPort:  80,
							Path:          "/test/path",
							LocalPathPort: 42,
							Protocol:      "tcp",
						},
					},
				},
				ExternalSNI: "test-external-sni",
			},
			false,
		},
		"expose.paths:matches": {
			&ServiceDefaults{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "my-test-service",
					Namespace: "consul-configs",
				},
				Spec: ServiceDefaultsSpec{
					Protocol: "https",
					MeshGateway: MeshGatewayConfig{
						Mode: "local",
					},
					Expose: ExposeConfig{
						Checks: true,
						Paths: []ExposePath{
							{
								ListenerPort:  80,
								Path:          "/test/path",
								LocalPathPort: 42,
								Protocol:      "tcp",
							},
							{
								ListenerPort:  8080,
								Path:          "/second/test/path",
								LocalPathPort: 11,
								Protocol:      "https",
							},
						},
					},
				},
			},
			&capi.ServiceConfigEntry{
				Kind:      capi.ServiceDefaults,
				Name:      "my-test-service",
				Namespace: "",
				Protocol:  "https",
				MeshGateway: capi.MeshGatewayConfig{
					Mode: capi.MeshGatewayModeLocal,
				},
				Expose: capi.ExposeConfig{
					Checks: true,
					Paths: []capi.ExposePath{
						{
							ListenerPort:  80,
							Path:          "/test/path",
							LocalPathPort: 42,
							Protocol:      "tcp",
						},
						{
							ListenerPort:  8080,
							Path:          "/second/test/path",
							LocalPathPort: 11,
							Protocol:      "https",
						},
					},
				},
			},
			true,
		},
		"expose.paths.listenerPort:mismatched": {
			&ServiceDefaults{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "my-test-service",
					Namespace: "consul-configs",
				},
				Spec: ServiceDefaultsSpec{
					Protocol: "https",
					MeshGateway: MeshGatewayConfig{
						Mode: "local",
					},
					Expose: ExposeConfig{
						Checks: true,
						Paths: []ExposePath{
							{
								ListenerPort:  80,
								Path:          "/test/path",
								LocalPathPort: 42,
								Protocol:      "tcp",
							},
						},
					},
				},
			},
			&capi.ServiceConfigEntry{
				Kind:      capi.ServiceDefaults,
				Name:      "my-test-service",
				Namespace: "",
				Protocol:  "https",
				MeshGateway: capi.MeshGatewayConfig{
					Mode: capi.MeshGatewayModeLocal,
				},
				Expose: capi.ExposeConfig{
					Checks: true,
					Paths: []capi.ExposePath{
						{
							ListenerPort:  81,
							Path:          "/test/path",
							LocalPathPort: 42,
							Protocol:      "tcp",
						},
					},
				},
			},
			false,
		},
		"expose.paths.path:mismatched": {
			&ServiceDefaults{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "my-test-service",
					Namespace: "consul-configs",
				},
				Spec: ServiceDefaultsSpec{
					Protocol: "https",
					MeshGateway: MeshGatewayConfig{
						Mode: "local",
					},
					Expose: ExposeConfig{
						Checks: true,
						Paths: []ExposePath{
							{
								ListenerPort:  80,
								Path:          "/test/path",
								LocalPathPort: 42,
								Protocol:      "tcp",
							},
						},
					},
				},
			},
			&capi.ServiceConfigEntry{
				Kind:      capi.ServiceDefaults,
				Name:      "my-test-service",
				Namespace: "",
				Protocol:  "https",
				MeshGateway: capi.MeshGatewayConfig{
					Mode: capi.MeshGatewayModeLocal,
				},
				Expose: capi.ExposeConfig{
					Checks: true,
					Paths: []capi.ExposePath{
						{
							ListenerPort:  80,
							Path:          "/differnt/path",
							LocalPathPort: 42,
							Protocol:      "tcp",
						},
					},
				},
			},
			false,
		},
		"expose.paths.localPathPort:mismatched": {
			&ServiceDefaults{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "my-test-service",
					Namespace: "consul-configs",
				},
				Spec: ServiceDefaultsSpec{
					Protocol: "https",
					MeshGateway: MeshGatewayConfig{
						Mode: "local",
					},
					Expose: ExposeConfig{
						Checks: true,
						Paths: []ExposePath{
							{
								ListenerPort:  80,
								Path:          "/test/path",
								LocalPathPort: 42,
								Protocol:      "tcp",
							},
						},
					},
				},
			},
			&capi.ServiceConfigEntry{
				Kind:      capi.ServiceDefaults,
				Name:      "my-test-service",
				Namespace: "",
				Protocol:  "https",
				MeshGateway: capi.MeshGatewayConfig{
					Mode: capi.MeshGatewayModeLocal,
				},
				Expose: capi.ExposeConfig{
					Checks: true,
					Paths: []capi.ExposePath{
						{
							ListenerPort:  80,
							Path:          "/test/path",
							LocalPathPort: 21,
							Protocol:      "tcp",
						},
					},
				},
			},
			false,
		},
		"expose.paths.protocol:mismatched": {
			&ServiceDefaults{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "my-test-service",
					Namespace: "consul-configs",
				},
				Spec: ServiceDefaultsSpec{
					Protocol: "https",
					MeshGateway: MeshGatewayConfig{
						Mode: "local",
					},
					Expose: ExposeConfig{
						Checks: true,
						Paths: []ExposePath{
							{
								ListenerPort:  80,
								Path:          "/test/path",
								LocalPathPort: 42,
								Protocol:      "tcp",
							},
						},
					},
				},
			},
			&capi.ServiceConfigEntry{
				Kind:      capi.ServiceDefaults,
				Name:      "my-test-service",
				Namespace: "",
				Protocol:  "https",
				MeshGateway: capi.MeshGatewayConfig{
					Mode: capi.MeshGatewayModeLocal,
				},
				Expose: capi.ExposeConfig{
					Checks: true,
					Paths: []capi.ExposePath{
						{
							ListenerPort:  80,
							Path:          "/test/path",
							LocalPathPort: 42,
							Protocol:      "https",
						},
					},
				},
			},
			false,
		},
		"expose.paths:mismatched when path lengths are different": {
			&ServiceDefaults{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "my-test-service",
					Namespace: "consul-configs",
				},
				Spec: ServiceDefaultsSpec{
					Protocol: "https",
					MeshGateway: MeshGatewayConfig{
						Mode: "local",
					},
					Expose: ExposeConfig{
						Checks: true,
						Paths: []ExposePath{
							{
								ListenerPort:  8080,
								Path:          "/second/test/path",
								LocalPathPort: 11,
								Protocol:      "https",
							},
							{
								ListenerPort:  80,
								Path:          "/test/path",
								LocalPathPort: 42,
								Protocol:      "tcp",
							},
						},
					},
					ExternalSNI: "test-external-sni",
				},
			},
			&capi.ServiceConfigEntry{
				Kind:      capi.ServiceDefaults,
				Name:      "my-test-service",
				Namespace: "",
				Protocol:  "https",
				MeshGateway: capi.MeshGatewayConfig{
					Mode: capi.MeshGatewayModeLocal,
				},
				Expose: capi.ExposeConfig{
					Checks: true,
					Paths: []capi.ExposePath{
						{
							ListenerPort:  8080,
							Path:          "/second/test/path",
							LocalPathPort: 11,
							Protocol:      "https",
						},
					},
				},
				ExternalSNI: "test-external-sni",
			},
			false,
		},
		"expose.paths:match when paths orders are different": {
			&ServiceDefaults{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "my-test-service",
					Namespace: "consul-configs",
				},
				Spec: ServiceDefaultsSpec{
					Protocol: "https",
					MeshGateway: MeshGatewayConfig{
						Mode: "local",
					},
					Expose: ExposeConfig{
						Checks: true,
						Paths: []ExposePath{
							{
								ListenerPort:  8080,
								Path:          "/second/test/path",
								LocalPathPort: 11,
								Protocol:      "https",
							},
							{
								ListenerPort:  80,
								Path:          "/test/path",
								LocalPathPort: 42,
								Protocol:      "tcp",
							},
						},
					},
					ExternalSNI: "test-external-sni",
				},
			},
			&capi.ServiceConfigEntry{
				Kind:      capi.ServiceDefaults,
				Name:      "my-test-service",
				Namespace: "",
				Protocol:  "https",
				MeshGateway: capi.MeshGatewayConfig{
					Mode: capi.MeshGatewayModeLocal,
				},
				Expose: capi.ExposeConfig{
					Checks: true,
					Paths: []capi.ExposePath{
						{
							ListenerPort:  80,
							Path:          "/test/path",
							LocalPathPort: 42,
							Protocol:      "tcp",
						},
						{
							ListenerPort:  8080,
							Path:          "/second/test/path",
							LocalPathPort: 11,
							Protocol:      "https",
						},
					},
				},
				ExternalSNI: "test-external-sni",
			},
			true,
		},
	}

	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			result := testCase.internal.MatchesConsul(testCase.consul)
			require.Equal(t, testCase.matches, result)
		})
	}
}
