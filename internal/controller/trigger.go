package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	finopsv1alpha1 "github.com/zapi-web/pod-idle-scaler/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type ActivityChecker interface {
	IsActive(ctx context.Context, scaler *finopsv1alpha1.IdleScaler) (bool, error)
}

type HTTPChecker struct {
	client     client.Reader
	httpClient *http.Client
}

type PromQLChecker struct {
	client     client.Reader
	httpClient *http.Client
}

type PromQLResponse struct {
	Status string `json:"status"`
	Data   struct {
		ResultType string `json:"resultType"`
		Result     []struct {
			Metric map[string]string `json:"metric"`
			Value  []any             `json:"value"`
		} `json:"result"`
	} `json:"data"`
}

func fetchSecret(ctx context.Context, c client.Reader, namespace string, ref *corev1.SecretKeySelector) (string, error) {
	if ref == nil {
		return "", nil
	}

	var secret corev1.Secret
	err := c.Get(ctx, types.NamespacedName{
		Namespace: namespace,
		Name:      ref.Name,
	}, &secret)

	if err != nil {
		return "", fmt.Errorf("failed to get a secret: %w", err)
	}

	tokenByte, ok := secret.Data[ref.Key]
	if !ok {
		return "", fmt.Errorf("key %q is not found in secret %s", ref.Key, ref.Name)
	}

	return strings.TrimSpace(string(tokenByte)), nil
}

func NewHTTPChecker(k8sClient client.Reader, httpClient *http.Client) *HTTPChecker {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &HTTPChecker{
		client:     k8sClient,
		httpClient: httpClient,
	}
}

func (h *HTTPChecker) IsActive(ctx context.Context, scaler *finopsv1alpha1.IdleScaler) (bool, error) {
	spec := scaler.Spec.Trigger.HTTP
	if spec == nil {
		return false, fmt.Errorf("http trigger spec is empty")
	}

	method := http.MethodGet
	if spec.Method != "" {
		method = spec.Method
	}

	timeout := 5 * time.Second
	if spec.Timeout != nil {
		timeout = spec.Timeout.Duration
	}

	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, method, spec.URL, nil)
	if err != nil {
		return false, fmt.Errorf("failed to build request: %w", err)
	}

	for k, v := range spec.Headers {
		req.Header.Set(k, v)
	}

	secret, err := fetchSecret(ctx, h.client, scaler.Namespace, spec.TokenSecretRef)

	if err != nil {
		return false, fmt.Errorf("failed to fetch Bearer Token: %w", err)
	}

	if secret != "" {
		req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", secret))
	}

	resp, err := h.httpClient.Do(req)
	if err != nil {
		return false, fmt.Errorf("failed to perform request: %w", err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()

	expectedCode := http.StatusOK
	if spec.ExpectedCode != nil {
		expectedCode = int(*spec.ExpectedCode)
	}

	if resp.StatusCode != expectedCode {
		if resp.StatusCode >= 500 && resp.StatusCode <= 599 {
			return false, fmt.Errorf("received server error: %d", resp.StatusCode)
		}
		return false, nil
	}

	if spec.BodyPattern != "" {
		body, err := io.ReadAll(io.LimitReader(resp.Body, 1024*1024))
		if err != nil {
			return false, fmt.Errorf("failed to read response body: %w", err)
		}

		re, err := regexp.Compile(spec.BodyPattern)
		if err != nil {
			return false, fmt.Errorf("invalid body regex %q: %w", spec.BodyPattern, err)
		}

		return re.Match(body), nil
	}

	return true, nil
}

func NewPromQLChecker(k8sClient client.Reader, httpClient *http.Client) *PromQLChecker {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &PromQLChecker{
		client:     k8sClient,
		httpClient: httpClient,
	}
}

func (p *PromQLChecker) IsActive(ctx context.Context, scaler *finopsv1alpha1.IdleScaler) (bool, error) {
	spec := scaler.Spec.Trigger.PromQL
	if spec == nil {
		return false, fmt.Errorf("promQL trigger is empty")
	}

	parsedURL, err := url.Parse(spec.URL)
	if err != nil {
		return false, fmt.Errorf("invalid url: %w", err)
	}

	args := parsedURL.Query()
	args.Set("query", spec.Query)
	parsedURL.RawQuery = args.Encode()

	timeout := 5 * time.Second
	if spec.Timeout != nil {
		timeout = spec.Timeout.Duration
	}

	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, parsedURL.String(), nil)
	if err != nil {
		return false, fmt.Errorf("failed to build request: %w", err)
	}

	secret, err := fetchSecret(ctx, p.client, scaler.Namespace, spec.TokenSecretRef)

	if err != nil {
		return false, fmt.Errorf("failed to fetch Bearer Token: %w", err)
	}

	if secret != "" {
		req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", secret))
	}

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return false, fmt.Errorf("failed to perform request: %w", err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("unexpected status code from promQL-API: %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1024*1024))
	if err != nil {
		return false, fmt.Errorf("failed to read response body: %w", err)
	}

	return evaluatePromQL(body, spec.Threshold)
}

func evaluatePromQL(body []byte, threshold *resource.Quantity) (bool, error) {
	var resp PromQLResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return false, fmt.Errorf("failed to unmarshmal response: %w", err)
	}

	if resp.Status != "success" {
		return false, errors.New("promQL query failed")
	}

	targetThreshold := 0.0
	if threshold != nil {
		targetThreshold = threshold.AsApproximateFloat64()
	}

	if len(resp.Data.Result) == 0 {
		return 0.0 > targetThreshold, nil
	}

	if len(resp.Data.Result) > 1 {
		return false, fmt.Errorf("query returned %d time series; aggregate using avg() or sum()", len(resp.Data.Result))
	}

	valArr := resp.Data.Result[0].Value
	if len(valArr) < 2 {
		return false, errors.New("invalid prometheus format")
	}

	valStr, ok := valArr[1].(string)
	if !ok {
		return false, errors.New("prometheus value is not a string")
	}

	val, err := strconv.ParseFloat(valStr, 64)
	if err != nil {
		return false, fmt.Errorf("failed to parse metric value %q: %w", valStr, err)
	}

	return val > targetThreshold, nil
}
