package main

import (
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base64"
	"fmt"
	"github.com/justinas/alice"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

var HMACAuthDef string = `

	{
		"name": "Tyk Test API",
		"api_id": "1",
		"org_id": "default",
		"definition": {
			"location": "header",
			"key": "version"
		},
		"enable_signature_checking": true,
        "hmac_allowed_clock_skew": 1000,
		"auth": {
			"auth_header_name": "authorization"
		},
		"version_data": {
			"not_versioned": true,
			"versions": {
				"Default": {
					"name": "Default",
					"expires": "3000-01-02 15:04",
					"paths": {
						"ignored": [],
						"white_list": [],
						"black_list": []
					}
				}
			}
		},
		"proxy": {
			"listen_path": "/v1",
			"target_url": "http://example.com/",
			"strip_listen_path": true
		}
	}

`

func createHMACAuthSession() SessionState {
	var thisSession SessionState
	thisSession.Rate = 8.0
	thisSession.Allowance = thisSession.Rate
	thisSession.LastCheck = time.Now().Unix()
	thisSession.Per = 1.0
	thisSession.Expires = 0
	thisSession.QuotaRenewalRate = 300 // 5 minutes
	thisSession.QuotaRenews = time.Now().Unix() + 20
	thisSession.QuotaRemaining = 1
	thisSession.QuotaMax = -1
	thisSession.HMACEnabled = true
	thisSession.HmacSecret = "9879879878787878"

	return thisSession
}

func getHMACAuthChain(spec APISpec) http.Handler {
	redisStore := RedisStorageManager{KeyPrefix: "apikey-"}
	healthStore := &RedisStorageManager{KeyPrefix: "apihealth."}
	orgStore := &RedisStorageManager{KeyPrefix: "orgKey."}
	spec.Init(&redisStore, &redisStore, healthStore, orgStore)
	remote, _ := url.Parse("http://lonelycode.com/")
	proxy := TykNewSingleHostReverseProxy(remote, &spec)
	proxyHandler := http.HandlerFunc(ProxyHandler(proxy, &spec))
	tykMiddleware := &TykMiddleware{&spec, proxy}
	chain := alice.New(
		CreateMiddleware(&IPWhiteListMiddleware{tykMiddleware}, tykMiddleware),
		CreateMiddleware(&HMACMiddleware{tykMiddleware}, tykMiddleware),
		CreateMiddleware(&VersionCheck{TykMiddleware: tykMiddleware}, tykMiddleware),
		CreateMiddleware(&KeyExpired{tykMiddleware}, tykMiddleware),
		CreateMiddleware(&AccessRightsCheck{tykMiddleware}, tykMiddleware),
		CreateMiddleware(&RateLimitAndQuotaCheck{tykMiddleware}, tykMiddleware)).Then(proxyHandler)

	return chain
}

func TestHMACAuthSession(t *testing.T) {
	spec := createDefinitionFromString(HMACAuthDef)
	redisStore := RedisStorageManager{KeyPrefix: "apikey-"}
	healthStore := &RedisStorageManager{KeyPrefix: "apihealth."}
	orgStore := &RedisStorageManager{KeyPrefix: "orgKey."}
	spec.Init(&redisStore, &redisStore, healthStore, orgStore)
	thisSession := createHMACAuthSession()

	// Basic auth sessions are stored as {org-id}{username}, so we need to append it here when we create the session.
	spec.SessionManager.UpdateSession("9876", thisSession, 60)

	uri := "/"
	method := "GET"

	recorder := httptest.NewRecorder()
	param := make(url.Values)
	req, err := http.NewRequest(method, uri+param.Encode(), nil)

	refDate := "Mon, 02 Jan 2006 15:04:05 MST"

	// Signature needs to be: Authorization: Signature keyId="hmac-key-1",algorithm="hmac-sha1",signature="Base64(HMAC-SHA1(signing string))"

	// Prep the signature string
	tim := time.Now().Format(refDate)
	req.Header.Add("Date", tim)
	signatureString := strings.ToLower("Date") + ":" + url.QueryEscape(tim)
	log.Debug("Signature string before encoding: ", signatureString)

	// Encode it
	key := []byte(thisSession.HmacSecret)
	h := hmac.New(sha1.New, key)
	h.Write([]byte(signatureString))

	sigString := base64.StdEncoding.EncodeToString(h.Sum(nil))
	encodedString := url.QueryEscape(sigString)
	log.Debug("Encoded signature string: ", encodedString)
	log.Debug("URL Encoded: ", url.QueryEscape(encodedString))

	log.Debug("Signature string: ", fmt.Sprintf("Signature keyId=\"9876\",algorithm=\"hmac-sha1\",signature=\"%s\"", encodedString))

	req.Header.Add("Authorization", fmt.Sprintf("Signature keyId=\"9876\",algorithm=\"hmac-sha1\",signature=\"%s\"", encodedString))

	if err != nil {
		t.Fatal(err)
	}

	chain := getHMACAuthChain(spec)
	chain.ServeHTTP(recorder, req)

	if recorder.Code != 200 {
		t.Error("Initial request failed with non-200 code, should have gone through!: \n", recorder.Code)
	}
}

func TestHMACAuthSessionFailureDateExpired(t *testing.T) {
	spec := createDefinitionFromString(HMACAuthDef)
	redisStore := RedisStorageManager{KeyPrefix: "apikey-"}
	healthStore := &RedisStorageManager{KeyPrefix: "apihealth."}
	orgStore := &RedisStorageManager{KeyPrefix: "orgKey."}
	spec.Init(&redisStore, &redisStore, healthStore, orgStore)
	thisSession := createHMACAuthSession()

	// Basic auth sessions are stored as {org-id}{username}, so we need to append it here when we create the session.
	spec.SessionManager.UpdateSession("9876", thisSession, 60)

	uri := "/"
	method := "GET"

	recorder := httptest.NewRecorder()
	param := make(url.Values)
	req, err := http.NewRequest(method, uri+param.Encode(), nil)

	refDate := "Mon, 02 Jan 2006 15:04:05 MST"

	// Signature needs to be: Authorization: Signature keyId="hmac-key-1",algorithm="hmac-sha1",signature="Base64(HMAC-SHA1(signing string))"

	// Prep the signature string
	tim := time.Now().Format(refDate)
	req.Header.Add("Date", tim)
	signatureString := strings.ToLower("Date") + ":" + url.QueryEscape(tim)
	log.Debug("Signature string before encoding: ", signatureString)

	// Encode it
	key := []byte(thisSession.HmacSecret)
	h := hmac.New(sha1.New, key)
	h.Write([]byte(signatureString))

	sigString := base64.StdEncoding.EncodeToString(h.Sum(nil))
	encodedString := url.QueryEscape(sigString)
	log.Debug("Encoded signature string: ", encodedString)
	log.Debug("URL Encoded: ", url.QueryEscape(encodedString))

	log.Debug("Signature string: ", fmt.Sprintf("Signature keyId=\"9876\",algorithm=\"hmac-sha1\",signature=\"%s\"", encodedString))

	req.Header.Add("Authorization", fmt.Sprintf("Signature keyId=\"9876\",algorithm=\"hmac-sha1\",signature=\"%s\"", encodedString))

	if err != nil {
		t.Fatal(err)
	}

	chain := getHMACAuthChain(spec)
	time.Sleep(time.Second * 2)
	chain.ServeHTTP(recorder, req)

	if recorder.Code != 400 {
		t.Error("Request should have failed with out of date error!: \n", recorder.Code)
	}
}

func TestHMACAuthSessionKeyMissing(t *testing.T) {
	spec := createDefinitionFromString(HMACAuthDef)
	redisStore := RedisStorageManager{KeyPrefix: "apikey-"}
	healthStore := &RedisStorageManager{KeyPrefix: "apihealth."}
	orgStore := &RedisStorageManager{KeyPrefix: "orgKey."}
	spec.Init(&redisStore, &redisStore, healthStore, orgStore)
	thisSession := createHMACAuthSession()

	// Basic auth sessions are stored as {org-id}{username}, so we need to append it here when we create the session.
	spec.SessionManager.UpdateSession("9876", thisSession, 60)

	uri := "/"
	method := "GET"

	recorder := httptest.NewRecorder()
	param := make(url.Values)
	req, err := http.NewRequest(method, uri+param.Encode(), nil)

	refDate := "Mon, 02 Jan 2006 15:04:05 MST"

	// Signature needs to be: Authorization: Signature keyId="hmac-key-1",algorithm="hmac-sha1",signature="Base64(HMAC-SHA1(signing string))"

	// Prep the signature string
	tim := time.Now().Format(refDate)
	req.Header.Add("Date", tim)
	signatureString := strings.ToLower("Date") + ":" + url.QueryEscape(tim)
	log.Debug("Signature string before encoding: ", signatureString)

	// Encode it
	key := []byte(thisSession.HmacSecret)
	h := hmac.New(sha1.New, key)
	h.Write([]byte(signatureString))

	sigString := base64.StdEncoding.EncodeToString(h.Sum(nil))
	encodedString := url.QueryEscape(sigString)
	log.Debug("Encoded signature string: ", encodedString)
	log.Debug("URL Encoded: ", url.QueryEscape(encodedString))

	log.Debug("Signature string: ", fmt.Sprintf("Signature keyId=\"98765\",algorithm=\"hmac-sha1\",signature=\"%s\"", encodedString))

	req.Header.Add("Authorization", fmt.Sprintf("Signature keyId=\"98765\",algorithm=\"hmac-sha1\",signature=\"%s\"", encodedString))

	if err != nil {
		t.Fatal(err)
	}

	chain := getHMACAuthChain(spec)
	time.Sleep(time.Second * 2)
	chain.ServeHTTP(recorder, req)

	if recorder.Code != 400 {
		t.Error("Request should have failed with key not found error!: \n", recorder.Code)
	}
}

func TestHMACAuthSessionmalformedHeader(t *testing.T) {
	spec := createDefinitionFromString(HMACAuthDef)
	redisStore := RedisStorageManager{KeyPrefix: "apikey-"}
	healthStore := &RedisStorageManager{KeyPrefix: "apihealth."}
	orgStore := &RedisStorageManager{KeyPrefix: "orgKey."}
	spec.Init(&redisStore, &redisStore, healthStore, orgStore)
	thisSession := createHMACAuthSession()

	// Basic auth sessions are stored as {org-id}{username}, so we need to append it here when we create the session.
	spec.SessionManager.UpdateSession("9876", thisSession, 60)

	uri := "/"
	method := "GET"

	recorder := httptest.NewRecorder()
	param := make(url.Values)
	req, err := http.NewRequest(method, uri+param.Encode(), nil)

	refDate := "Mon, 02 Jan 2006 15:04:05 MST"

	// Signature needs to be: Authorization: Signature keyId="hmac-key-1",algorithm="hmac-sha1",signature="Base64(HMAC-SHA1(signing string))"

	// Prep the signature string
	tim := time.Now().Format(refDate)
	req.Header.Add("Date", tim)
	signatureString := strings.ToLower("Date") + ":" + url.QueryEscape(tim)
	log.Debug("Signature string before encoding: ", signatureString)

	// Encode it
	key := []byte(thisSession.HmacSecret)
	h := hmac.New(sha1.New, key)
	h.Write([]byte(signatureString))

	sigString := base64.StdEncoding.EncodeToString(h.Sum(nil))
	encodedString := url.QueryEscape(sigString)
	log.Debug("Encoded signature string: ", encodedString)
	log.Debug("URL Encoded: ", url.QueryEscape(encodedString))

	log.Debug("Signature string: ", fmt.Sprintf("Signature keyID=\"98765\", algorithm=\"hmac-sha1\", signature=\"%s\"", encodedString))

	req.Header.Add("Authorization", fmt.Sprintf("Signature keyID=\"98765\", algorithm=\"hmac-sha256\", signature=\"%s\"", encodedString))

	if err != nil {
		t.Fatal(err)
	}

	chain := getHMACAuthChain(spec)
	time.Sleep(time.Second * 2)
	chain.ServeHTTP(recorder, req)

	if recorder.Code != 400 {
		t.Error("Request should have failed with key not found error!: \n", recorder.Code)
	}
}
