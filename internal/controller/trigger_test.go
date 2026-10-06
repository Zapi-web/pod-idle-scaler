package controller

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	finopsv1alpha1 "github.com/zapi-web/pod-idle-scaler/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func newFakeClient(t *testing.T, objs ...client.Object) client.Client {
	t.Helper()

	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add corev1 to scheme: %v", err)
	}

	return fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objs...).
		Build()
}

func newHTTPScaler(ns, url string, mutate func(*finopsv1alpha1.HTTPTriggerSpec)) *finopsv1alpha1.IdleScaler {
	spec := &finopsv1alpha1.HTTPTriggerSpec{URL: url}
	if mutate != nil {
		mutate(spec)
	}
	return &finopsv1alpha1.IdleScaler{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns},
		Spec: finopsv1alpha1.IdleScalerSpec{
			Trigger: finopsv1alpha1.TriggerSpec{
				Type: finopsv1alpha1.TriggerTypeHTTP,
				HTTP: spec,
			},
		},
	}
}

func newPromQLScaler(ns, url string, mutate func(*finopsv1alpha1.PromQLTriggerSpec)) *finopsv1alpha1.IdleScaler {
	spec := &finopsv1alpha1.PromQLTriggerSpec{URL: url, Query: "up"}
	if mutate != nil {
		mutate(spec)
	}
	return &finopsv1alpha1.IdleScaler{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns},
		Spec: finopsv1alpha1.IdleScalerSpec{
			Trigger: finopsv1alpha1.TriggerSpec{
				Type:   finopsv1alpha1.TriggerTypePromQL,
				PromQL: spec,
			},
		},
	}
}

func TestFetchSecret(t *testing.T) {
	const (
		testKey          = "some-key"
		testSecret       = "some-secret"
		notTrimmedSecret = "\n\t  some-secret \r\n"
		defNamespace     = "default"
		otherNamespace   = "notDefNamespace"
		testName         = "some-name"
	)

	newSecret := func(ns string, data map[string][]byte) *corev1.Secret {
		return &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: testName, Namespace: ns},
			Data:       data,
		}
	}
	validSelector := &corev1.SecretKeySelector{
		LocalObjectReference: corev1.LocalObjectReference{Name: testName},
		Key:                  testKey,
	}

	tests := []struct {
		name           string
		namespace      string
		secretObj      *corev1.Secret
		selector       *corev1.SecretKeySelector
		expectedResult string
		expectError    bool
	}{
		{
			name:           "Clean Path",
			namespace:      defNamespace,
			secretObj:      newSecret(defNamespace, map[string][]byte{testKey: []byte(testSecret)}),
			selector:       validSelector,
			expectedResult: testSecret,
		},
		{
			name:           "Nil Selector Reference",
			namespace:      defNamespace,
			secretObj:      newSecret(defNamespace, map[string][]byte{testKey: []byte(testSecret)}),
			selector:       nil,
			expectedResult: "",
		},
		{
			name:           "Trim Spaces",
			namespace:      defNamespace,
			secretObj:      newSecret(defNamespace, map[string][]byte{testKey: []byte(notTrimmedSecret)}),
			selector:       validSelector,
			expectedResult: testSecret,
		},
		{
			name:           "Wrong Namespace",
			namespace:      defNamespace,
			secretObj:      newSecret(otherNamespace, map[string][]byte{testKey: []byte(testSecret)}),
			selector:       validSelector,
			expectedResult: "",
			expectError:    true,
		},
		{
			name:           "Secret Not Found",
			namespace:      defNamespace,
			secretObj:      nil,
			selector:       validSelector,
			expectedResult: "",
			expectError:    true,
		},
		{
			name:           "Key Not Found",
			namespace:      defNamespace,
			secretObj:      newSecret(defNamespace, map[string][]byte{"other-key": []byte("x")}),
			selector:       validSelector,
			expectedResult: "",
			expectError:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var objs []client.Object
			if tt.secretObj != nil {
				objs = append(objs, tt.secretObj)
			}
			reader := newFakeClient(t, objs...)

			got, err := fetchSecret(context.Background(), reader, tt.namespace, tt.selector)

			if tt.expectError && err == nil {
				t.Fatalf("fetchSecret(): expected error, got nil")
			}
			if !tt.expectError && err != nil {
				t.Fatalf("fetchSecret(): unexpected error: %v", err)
			}

			if got != tt.expectedResult {
				t.Fatalf("fetchSecret(): want %q, got %q", tt.expectedResult, got)
			}
		})
	}
}

func TestHTTPChecker_IsActive(t *testing.T) {
	const ns = "default"

	tests := []struct {
		name         string
		code         int
		body         string
		mutate       func(*finopsv1alpha1.HTTPTriggerSpec)
		expectMethod string
		want         bool
		wantErr      bool
	}{
		{
			name:         "200 ok, default GET",
			code:         http.StatusOK,
			expectMethod: http.MethodGet,
			want:         true,
		},
		{
			name: "explicit POST",
			code: http.StatusOK,
			mutate: func(s *finopsv1alpha1.HTTPTriggerSpec) {
				s.Method = http.MethodPost
			},
			expectMethod: http.MethodPost,
			want:         true,
		},
		{
			name:         "server error",
			code:         http.StatusInternalServerError,
			expectMethod: http.MethodGet,
			want:         false,
			wantErr:      true,
		},
		{
			name:         "client error is not an error",
			code:         http.StatusNotFound,
			expectMethod: http.MethodGet,
			want:         false,
			wantErr:      false,
		},
		{
			name: "body pattern mismatch",
			code: http.StatusOK,
			body: `{"status":"down"}`,
			mutate: func(s *finopsv1alpha1.HTTPTriggerSpec) {
				s.BodyPattern = `"status":"up"`
			},
			expectMethod: http.MethodGet,
			want:         false,
		},
		{
			name: "body pattern match",
			code: http.StatusOK,
			body: `{"status":"up"}`,
			mutate: func(s *finopsv1alpha1.HTTPTriggerSpec) {
				s.BodyPattern = `"status":"up"`
			},
			expectMethod: http.MethodGet,
			want:         true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			methodCh := make(chan string, 1)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				methodCh <- r.Method
				w.WriteHeader(tt.code)
				if tt.body != "" {
					_, _ = w.Write([]byte(tt.body))
				}
			}))
			defer srv.Close()

			scaler := newHTTPScaler(ns, srv.URL, tt.mutate)

			checker := NewHTTPChecker(newFakeClient(t), srv.Client())
			got, err := checker.IsActive(t.Context(), scaler)
			if tt.wantErr && err == nil {
				t.Fatal("HTTPChecker.IsActive(): expected error, got nil")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("HTTPChecker.IsActive(): unexpected error: %v", err)
			}

			var gotMethod string
			select {
			case gotMethod = <-methodCh:
			default:
				t.Fatal("HTTPChecker.IsActive(): handler was not called")
			}
			if got != tt.want {
				t.Fatalf("HTTPChecker.IsActive(): want %v, got %v", tt.want, got)
			}
			if tt.expectMethod != "" && gotMethod != tt.expectMethod {
				t.Errorf("HTTPChecker.IsActive(): method: want %q, got %q", tt.expectMethod, gotMethod)
			}
		})
	}
}

func TestHTTPChecker_IsActive_NilSpec(t *testing.T) {
	scaler := &finopsv1alpha1.IdleScaler{
		ObjectMeta: metav1.ObjectMeta{Namespace: "default"},
		Spec: finopsv1alpha1.IdleScalerSpec{
			Trigger: finopsv1alpha1.TriggerSpec{Type: finopsv1alpha1.TriggerTypeHTTP},
		},
	}
	checker := NewHTTPChecker(newFakeClient(t), http.DefaultClient)
	if _, err := checker.IsActive(t.Context(), scaler); err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestHTTPChecker_IsActive_BearerFromSecret(t *testing.T) {
	const (
		ns         = "default"
		secretName = "auth-token"
		secretKey  = "token"
		secretVal  = "s3cr3t"
	)

	authCh := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authCh <- r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: secretName, Namespace: ns},
		Data:       map[string][]byte{secretKey: []byte(secretVal)},
	}

	scaler := newHTTPScaler(ns, srv.URL, func(s *finopsv1alpha1.HTTPTriggerSpec) {
		s.TokenSecretRef = &corev1.SecretKeySelector{
			LocalObjectReference: corev1.LocalObjectReference{Name: secretName},
			Key:                  secretKey,
		}
	})

	checker := NewHTTPChecker(newFakeClient(t, secret), srv.Client())
	got, err := checker.IsActive(t.Context(), scaler)
	if err != nil {
		t.Fatalf("HTTPChecker.IsActive(): unexpected error: %v", err)
	}

	var gotAuth string
	select {
	case gotAuth = <-authCh:
	default:
		t.Fatal("HTTPChecker.IsActive(): handler was not called")
	}
	if !got {
		t.Fatal("HTTPChecker.IsActive(): expected active")
	}
	if want := "Bearer " + secretVal; gotAuth != want {
		t.Fatalf("HTTPChecker.IsActive(): Authorization: want %q, got %q", want, gotAuth)
	}
}

func TestEvaluatePromQL(t *testing.T) {
	q := func(s string) *resource.Quantity {
		v := resource.MustParse(s)
		return &v
	}
	series := func(val string) []byte {
		return fmt.Appendf(nil,
			`{"status":"success","data":{"resultType":"vector","result":[{"metric":{},"value":[1,%q]}]}}`,
			val,
		)
	}

	tests := []struct {
		name           string
		threshold      *resource.Quantity
		body           []byte
		expectedResult bool
		expectError    bool
	}{
		{
			name:           "value above threshold",
			threshold:      q("5"),
			body:           series("10"),
			expectedResult: true,
		},
		{
			name:           "value equal to threshold is not active",
			threshold:      q("5"),
			body:           series("5"),
			expectedResult: false,
		},
		{
			name:           "empty result is not active",
			body:           []byte(`{"status":"success","data":{"resultType":"vector","result":[]}}`),
			expectedResult: false,
		},
		{
			name:        "non-success status is an error",
			body:        []byte(`{"status":"error","data":{}}`),
			expectError: true,
		},
		{
			name:        "invalid json is an error",
			body:        []byte(`not-json`),
			expectError: true,
		},
		{
			name: "multiple series is an error",
			body: []byte(`{"status":"success","data":{"resultType":"vector","result":[` +
				`{"metric":{},"value":[1,"1"]},{"metric":{},"value":[1,"2"]}]}}`),
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res, err := evaluatePromQL(tt.body, tt.threshold)

			if tt.expectError && err == nil {
				t.Fatalf("evaluatePromQL(): expected error, got nil")
			}
			if !tt.expectError && err != nil {
				t.Fatalf("evaluatePromQL(): unexpected error: %v", err)
			}

			if res != tt.expectedResult {
				t.Fatalf("evaluatePromQL(): want %v, got %v", tt.expectedResult, res)
			}
		})
	}
}

func TestPromQLChecker_IsActive(t *testing.T) {
	const ns = "default"

	tests := []struct {
		name       string
		mutate     func(*finopsv1alpha1.PromQLTriggerSpec)
		secretObjs []client.Object
		code       int
		body       string
		wantQuery  string
		wantAuth   string
		want       bool
		wantErr    bool
	}{
		{
			name: "query is forwarded, value above threshold",
			mutate: func(s *finopsv1alpha1.PromQLTriggerSpec) {
				s.Query = "sum(rate(http_requests_total[5m]))"
			},
			code:      http.StatusOK,
			body:      `{"status":"success","data":{"resultType":"vector","result":[{"metric":{},"value":[1,"42"]}]}}`,
			wantQuery: "sum(rate(http_requests_total[5m]))",
			wantAuth:  "",
			want:      true,
		},
		{
			name:      "non-200 is an error",
			code:      http.StatusNotFound,
			wantQuery: "up",
			wantAuth:  "",
			want:      false,
			wantErr:   true,
		},
		{
			name: "bearer from secret",
			mutate: func(s *finopsv1alpha1.PromQLTriggerSpec) {
				s.TokenSecretRef = &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: "auth"},
					Key:                  "token",
				}
			},
			secretObjs: []client.Object{&corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: "auth", Namespace: ns},
				Data:       map[string][]byte{"token": []byte("s3cr3t")},
			}},
			code:      http.StatusOK,
			body:      `{"status":"success","data":{"resultType":"vector","result":[]}}`,
			wantQuery: "up",
			wantAuth:  "Bearer s3cr3t",
			want:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			type capture struct{ query, auth string }
			capturedCh := make(chan capture, 1)

			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				capturedCh <- capture{
					query: r.URL.Query().Get("query"),
					auth:  r.Header.Get("Authorization"),
				}
				w.WriteHeader(tt.code)
				if tt.body != "" {
					_, _ = w.Write([]byte(tt.body))
				}
			}))
			defer srv.Close()

			scaler := newPromQLScaler(ns, srv.URL, tt.mutate)
			checker := NewPromQLChecker(newFakeClient(t, tt.secretObjs...), srv.Client())

			got, err := checker.IsActive(t.Context(), scaler)
			if tt.wantErr && err == nil {
				t.Fatal("expected error, got nil")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("want %v, got %v", tt.want, got)
			}

			var c capture
			select {
			case c = <-capturedCh:
			default:
				t.Fatal("handler was not called")
			}
			if c.query != tt.wantQuery {
				t.Errorf("query: want %q, got %q", tt.wantQuery, c.query)
			}
			if c.auth != tt.wantAuth {
				t.Errorf("auth: want %q, got %q", tt.wantAuth, c.auth)
			}
		})
	}
}
