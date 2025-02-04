package main

import (
	"errors"
	"github.com/garyburd/redigo/redis"
	"github.com/lonelycode/go-uuid/uuid"
	"github.com/lonelycode/gorpc"
	"github.com/pmylund/go-cache"
	"io"
	"strings"
	"time"
)

type InboundData struct {
	KeyName      string
	Value        string
	SessionState string
	Timeout      int64
	Per          int64
	Expire       int64
}

type DefRequest struct {
	OrgId string
	Tags  []string
}

type KeysValuesPair struct {
	Keys   []string
	Values []string
}

var ErrorDenied error = errors.New("Access Denied")

// ------------------- CLOUD STORAGE MANAGER -------------------------------

var RPCClients = map[string]chan int{}

func ClearRPCClients() {
	for _, c := range RPCClients {
		c <- 1
	}
}

// RPCStorageHandler is a storage manager that uses the redis database.
type RPCStorageHandler struct {
	RPCClient        *gorpc.Client
	Client           *gorpc.DispatcherClient
	KeyPrefix        string
	HashKeys         bool
	UserKey          string
	Address          string
	cache            *cache.Cache
	killChan         chan int
	Connected        bool
	ID               string
	SuppressRegister bool
}

func (r *RPCStorageHandler) Register() {
	r.ID = uuid.NewUUID().String()
	myChan := make(chan int)
	RPCClients[r.ID] = myChan
	r.killChan = myChan
	log.Debug("RPC Client registered")
}

func (r *RPCStorageHandler) checkDisconnect() {
	select {
	case res := <-r.killChan:
		log.Debug("RPC Client disconnecting: ", res)
		r.Disconnect()
	}
}

// Connect will establish a connection to the DB
func (r *RPCStorageHandler) Connect() bool {
	// Set up the cache
	r.cache = cache.New(30*time.Second, 15*time.Second)
	r.RPCClient = gorpc.NewTCPClient(r.Address)
	r.RPCClient.OnConnect = r.OnConnectFunc
	r.RPCClient.Conns = 10
	r.RPCClient.Start()
	d := GetDispatcher()
	r.Client = d.NewFuncClient(r.RPCClient)
	r.Login()

	if !r.SuppressRegister {
		r.Register()
		go r.checkDisconnect()
	}

	return true
}

func (r *RPCStorageHandler) OnConnectFunc(remoteAddr string, rwc io.ReadWriteCloser) (io.ReadWriteCloser, error) {
	r.Connected = true
	return rwc, nil
}

func (r *RPCStorageHandler) Disconnect() bool {
	if r.Connected {
		r.RPCClient.Stop()
		r.Connected = false
		delete(RPCClients, r.ID)
	}
	return true
}

func (r *RPCStorageHandler) hashKey(in string) string {
	if !r.HashKeys {
		// Not hashing? Return the raw key
		return in
	}
	return doHash(in)
}

func (r *RPCStorageHandler) fixKey(keyName string) string {
	setKeyName := r.KeyPrefix + r.hashKey(keyName)

	log.Debug("Input key was: ", setKeyName)

	return setKeyName
}

func (r *RPCStorageHandler) cleanKey(keyName string) string {
	setKeyName := strings.Replace(keyName, r.KeyPrefix, "", 1)
	return setKeyName
}

func (r *RPCStorageHandler) Login() {
	log.Debug("[RPC Store] Login initiated")

	if len(r.UserKey) == 0 {
		log.Fatal("No API Key set!")
	}

	ok, err := r.Client.Call("Login", r.UserKey)
	if err != nil {
		log.Fatal("RPC Login failed: ", err)
	}

	if !ok.(bool) {
		log.Fatal("RPC Login incorrect")
	}
	log.Debug("[RPC Store] Login complete")
}

// GetKey will retreive a key from the database
func (r *RPCStorageHandler) GetKey(keyName string) (string, error) {
	start := time.Now() // get current time
	log.Debug("[STORE] Getting WAS: ", keyName)
	log.Debug("[STORE] Getting: ", r.fixKey(keyName))

	// Check the cache first
	if config.SlaveOptions.EnableRPCCache {
		cachedVal, found := r.cache.Get(r.fixKey(keyName))
		if found {
			elapsed := time.Since(start)
			log.Debug("GetKey took ", elapsed)
			log.Debug(cachedVal.(string))
			return cachedVal.(string), nil
		}
	}

	// Not cached
	value, err := r.Client.Call("GetKey", r.fixKey(keyName))

	if err != nil {
		if r.IsAccessError(err) {
			r.Login()
			return r.GetKey(keyName)
		}

		log.Debug("Error trying to get value:", err)
		return "", KeyError{}
	}
	elapsed := time.Since(start)
	log.Debug("GetKey took ", elapsed)

	if config.SlaveOptions.EnableRPCCache {
		// Cache it
		r.cache.Set(r.fixKey(keyName), value, cache.DefaultExpiration)
	}

	return value.(string), nil
}

func (r *RPCStorageHandler) GetRawKey(keyName string) (string, error) {
	log.Error("Not Implemented!")

	return "", nil
}

func (r *RPCStorageHandler) GetExp(keyName string) (int64, error) {
	log.Debug("GetExp called")
	value, err := r.Client.Call("GetExp", r.fixKey(keyName))

	if err != nil {
		if r.IsAccessError(err) {
			r.Login()
			return r.GetExp(keyName)
		}
		log.Error("Error trying to get TTL: ", err)
	} else {
		return value.(int64), nil
	}

	return 0, KeyError{}
}

// SetKey will create (or update) a key value in the store
func (r *RPCStorageHandler) SetKey(keyName string, sessionState string, timeout int64) error {
	start := time.Now() // get current time
	ibd := InboundData{
		KeyName:      r.fixKey(keyName),
		SessionState: sessionState,
		Timeout:      timeout,
	}

	_, err := r.Client.Call("SetKey", ibd)

	if r.IsAccessError(err) {
		r.Login()
		return r.SetKey(keyName, sessionState, timeout)
	}

	elapsed := time.Since(start)
	log.Debug("SetKey took ", elapsed)
	return nil

}

func (r *RPCStorageHandler) SetRawKey(keyName string, sessionState string, timeout int64) error {
	return nil
}

// Decrement will decrement a key in redis
func (r *RPCStorageHandler) Decrement(keyName string) {
	log.Warning("Decrement called")
	_, err := r.Client.Call("Decrement", keyName)
	if r.IsAccessError(err) {
		r.Login()
		r.Decrement(keyName)
		return
	}
}

// IncrementWithExpire will increment a key in redis
func (r *RPCStorageHandler) IncrememntWithExpire(keyName string, expire int64) int64 {

	ibd := InboundData{
		KeyName: keyName,
		Expire:  expire,
	}

	val, err := r.Client.Call("IncrememntWithExpire", ibd)

	if r.IsAccessError(err) {
		r.Login()
		return r.IncrememntWithExpire(keyName, expire)
	}

	return val.(int64)

}

// GetKeys will return all keys according to the filter (filter is a prefix - e.g. tyk.keys.*)
func (r *RPCStorageHandler) GetKeys(filter string) []string {

	log.Error("GetKeys Not Implemented")

	return []string{}
}

// GetKeysAndValuesWithFilter will return all keys and their values with a filter
func (r *RPCStorageHandler) GetKeysAndValuesWithFilter(filter string) map[string]string {

	searchStr := r.KeyPrefix + r.hashKey(filter) + "*"
	log.Debug("[STORE] Getting list by: ", searchStr)

	kvPair, err := r.Client.Call("GetKeysAndValuesWithFilter", searchStr)

	if r.IsAccessError(err) {
		r.Login()
		return r.GetKeysAndValuesWithFilter(filter)
	}

	returnValues := make(map[string]string)

	for i, v := range kvPair.(*KeysValuesPair).Keys {
		returnValues[r.cleanKey(v)] = kvPair.(*KeysValuesPair).Values[i]
	}

	return returnValues
}

// GetKeysAndValues will return all keys and their values - not to be used lightly
func (r *RPCStorageHandler) GetKeysAndValues() map[string]string {

	searchStr := r.KeyPrefix + "*"
	kvPair, err := r.Client.Call("GetKeysAndValues", searchStr)

	if r.IsAccessError(err) {
		r.Login()
		return r.GetKeysAndValues()
	}

	returnValues := make(map[string]string)
	for i, v := range kvPair.(*KeysValuesPair).Keys {
		returnValues[r.cleanKey(v)] = kvPair.(*KeysValuesPair).Values[i]
	}

	return returnValues

}

// DeleteKey will remove a key from the database
func (r *RPCStorageHandler) DeleteKey(keyName string) bool {

	log.Debug("DEL Key was: ", keyName)
	log.Debug("DEL Key became: ", r.fixKey(keyName))
	ok, err := r.Client.Call("DeleteKey", r.fixKey(keyName))

	if r.IsAccessError(err) {
		r.Login()
		return r.DeleteKey(keyName)
	}

	return ok.(bool)
}

// DeleteKey will remove a key from the database without prefixing, assumes user knows what they are doing
func (r *RPCStorageHandler) DeleteRawKey(keyName string) bool {
	ok, err := r.Client.Call("DeleteRawKey", keyName)

	if r.IsAccessError(err) {
		r.Login()
		return r.DeleteRawKey(keyName)
	}

	return ok.(bool)
}

// DeleteKeys will remove a group of keys in bulk
func (r *RPCStorageHandler) DeleteKeys(keys []string) bool {
	if len(keys) > 0 {
		asInterface := make([]string, len(keys))
		for i, v := range keys {
			asInterface[i] = r.fixKey(v)
		}

		log.Debug("Deleting: ", asInterface)
		ok, err := r.Client.Call("DeleteKeys", asInterface)

		if r.IsAccessError(err) {
			r.Login()
			return r.DeleteKeys(keys)
		}

		return ok.(bool)
	} else {
		log.Debug("RPCStorageHandler called DEL - Nothing to delete")
		return true
	}

	return true
}

// DeleteKeys will remove a group of keys in bulk without a prefix handler
func (r *RPCStorageHandler) DeleteRawKeys(keys []string, prefix string) bool {
	log.Error("DeleteRawKeys Not Implemented")
	return false
}

// StartPubSubHandler will listen for a signal and run the callback with the message
func (r *RPCStorageHandler) StartPubSubHandler(channel string, callback func(redis.Message)) error {
	log.Warning("NO PUBSUB DEFINED")
	return nil
}

func (r *RPCStorageHandler) Publish(channel string, message string) error {
	log.Warning("NO PUBSUB DEFINED")
	return nil
}

func (r *RPCStorageHandler) GetAndDeleteSet(keyName string) []interface{} {
	log.Error("GetAndDeleteSet Not implemented, please disable your purger")

	return []interface{}{}
}

func (r *RPCStorageHandler) AppendToSet(keyName string, value string) {

	ibd := InboundData{
		KeyName: keyName,
		Value:   value,
	}

	_, err := r.Client.Call("AppendToSet", ibd)
	if r.IsAccessError(err) {
		r.Login()
		r.AppendToSet(keyName, value)
		return
	}

}

// SetScrollingWindow is used in the rate limiter to handle rate limits fairly.
func (r *RPCStorageHandler) SetRollingWindow(keyName string, per int64, expire int64) int {
	start := time.Now() // get current time
	ibd := InboundData{
		KeyName: keyName,
		Per:     per,
		Expire:  expire,
	}

	intVal, err := r.Client.Call("SetRollingWindow", ibd)
	if r.IsAccessError(err) {
		r.Login()
		return r.SetRollingWindow(keyName, per, expire)
	}

	elapsed := time.Since(start)
	log.Debug("SetRollingWindow took ", elapsed)

	return intVal.(int)

}

func (r RPCStorageHandler) IsAccessError(err error) bool {
	if err != nil {
		if err.Error() == "Access Denied" {
			return true
		}
		return false
	}
	return false
}

// GetAPIDefinitions will pull API definitions from the RPC server
func (r *RPCStorageHandler) GetApiDefinitions(orgId string, tags []string) string {
	dr := DefRequest{
		OrgId: orgId,
		Tags:  tags,
	}

	defString, err := r.Client.Call("GetApiDefinitions", dr)

	if err != nil {
		if r.IsAccessError(err) {
			r.Login()
			return r.GetApiDefinitions(orgId, tags)
		}
	}
	log.Debug("API Definitions retrieved")
	return defString.(string)

}

// GetPolicies will pull Policies from the RPC server
func (r *RPCStorageHandler) GetPolicies(orgId string) string {
	defString, err := r.Client.Call("GetPolicies", orgId)
	if err != nil {
		if r.IsAccessError(err) {
			r.Login()
			return r.GetPolicies(orgId)
		}
	}

	return defString.(string)

}

// CheckForReload will start a long poll
func (r *RPCStorageHandler) CheckForReload(orgId string) {
	log.Debug("[RPC STORE] Check Reload called...")
	reload, err := r.Client.CallTimeout("CheckReload", orgId, time.Second*60)
	if err != nil {
		if r.IsAccessError(err) {
			log.Warning("[RPC STORE] CheckReload: Not logged in")
			r.Login()
		}
	} else {
		log.Debug("[RPC STORE] CheckReload: Received response")
		if reload.(bool) {
			// Do the reload!
			log.Warning("[RPC STORE] Received Reload instruction!")
			go ReloadURLStructure()
		}
	}

}

func (r *RPCStorageHandler) StartRPCLoopCheck(orgId string) {
	log.Info("Starting keyspace poller")

	for {
		r.CheckForKeyspaceChanges(orgId)
		time.Sleep(30 * time.Second)
	}
}

// CheckForKeyspaceChanges will poll for keysace changes
func (r *RPCStorageHandler) CheckForKeyspaceChanges(orgId string) {
	keys, err := r.Client.Call("GetKeySpaceUpdate", orgId)

	if err != nil {
		if r.IsAccessError(err) {
			r.Login()
			r.CheckForKeyspaceChanges(orgId)
		}
	}

	if keys == nil {
		log.Error("Keys returned nil object, skipping check")
		return
	}

	if len(keys.([]string)) > 0 {
		log.Info("Keyspace changes detected, updating local cache")
		go r.ProcessKeySpaceChanges(keys.([]string))
	}
}

func (r *RPCStorageHandler) ProcessKeySpaceChanges(keys []string) {
	for _, key := range keys {
		log.Info("--> removing cached key: ", key)
		handleDeleteKey(key, "-1")
	}
}

func GetDispatcher() *gorpc.Dispatcher {
	var Dispatch *gorpc.Dispatcher = gorpc.NewDispatcher()

	Dispatch.AddFunc("Login", func(clientAddr string, userKey string) bool {
		return false
	})

	Dispatch.AddFunc("GetKey", func(keyName string) (string, error) {
		return "", nil
	})

	Dispatch.AddFunc("SetKey", func(ibd *InboundData) error {
		return nil
	})

	Dispatch.AddFunc("GetExp", func(keyName string) (int64, error) {
		return 0, nil
	})

	Dispatch.AddFunc("GetKeys", func(keyName string) ([]string, error) {
		return []string{}, nil
	})

	Dispatch.AddFunc("DeleteKey", func(keyName string) (bool, error) {
		return true, nil
	})

	Dispatch.AddFunc("DeleteRawKey", func(keyName string) (bool, error) {
		return true, nil
	})

	Dispatch.AddFunc("GetKeysAndValues", func(searchString string) (*KeysValuesPair, error) {
		return nil, nil
	})

	Dispatch.AddFunc("GetKeysAndValuesWithFilter", func(searchString string) (*KeysValuesPair, error) {
		return nil, nil
	})

	Dispatch.AddFunc("DeleteKeys", func(keys []string) (bool, error) {
		return true, nil
	})

	Dispatch.AddFunc("Decrement", func(keyName string) error {
		return nil
	})

	Dispatch.AddFunc("IncrememntWithExpire", func(ibd *InboundData) (int64, error) {
		return 0, nil
	})

	Dispatch.AddFunc("AppendToSet", func(ibd *InboundData) error {
		return nil
	})

	Dispatch.AddFunc("SetRollingWindow", func(ibd *InboundData) (int, error) {
		return 0, nil
	})

	Dispatch.AddFunc("GetApiDefinitions", func(dr *DefRequest) (string, error) {
		return "", nil
	})

	Dispatch.AddFunc("GetPolicies", func(orgId string) (string, error) {
		return "", nil
	})

	Dispatch.AddFunc("PurgeAnalyticsData", func(data string) error {
		return nil
	})

	Dispatch.AddFunc("CheckReload", func(clientAddr string, orgId string) (bool, error) {
		return false, nil
	})

	Dispatch.AddFunc("GetKeySpaceUpdate", func(clientAddr string, orgId string) ([]string, error) {
		return []string{}, nil
	})

	return Dispatch

}
