package gateway

import (
	"errors"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/http/httputil"
	"net/url"
	"regexp"
	"strings"
	"sync"

	"github.com/gorilla/mux"
	"github.com/moleculer-go/moleculer"
	"github.com/moleculer-go/moleculer/payload"
	"github.com/moleculer-go/moleculer/serializer"
	"github.com/moleculer-go/moleculer/service"
	"github.com/rs/cors"
	log "github.com/sirupsen/logrus"
)

var actionWildCardRegex = regexp.MustCompile(`(.+)\.\*`)
var serviceWildCardRegex = regexp.MustCompile(`\*\.(.+)`)
var serviceActionRegex = regexp.MustCompile(`(.+)\.(.+)`)

//shouldInclude check if the actions should be added based on the whitelist.
func shouldInclude(whitelist []string, action string) bool {
	for _, item := range whitelist {
		if item == "**" || item == "*.*" {
			return true
		}
		whitelistService := actionWildCardRegex.FindStringSubmatch(item)
		if len(whitelistService) > 0 && whitelistService[1] != "" {
			actionService := serviceActionRegex.FindStringSubmatch(action)
			if len(actionService) > 1 && len(whitelistService) > 1 && actionService[1] == whitelistService[1] {
				return true
			}
		}
		whitelistAction := serviceWildCardRegex.FindStringSubmatch(item)
		if len(whitelistAction) > 0 && whitelistAction[1] != "" {
			actionName := serviceActionRegex.FindStringSubmatch(action)
			if len(actionName) > 2 && len(whitelistAction) > 1 && actionName[2] == whitelistAction[1] {
				return true
			}
		}
		itemRegex, err := regexp.Compile(item)
		if err == nil {
			if itemRegex.MatchString(action) {
				return true
			}
		}
	}
	return false
}

type actionHandler struct {
	routePath            string
	alias                string
	action               string
	context              moleculer.Context
	acceptedMethodsCache map[string]bool
}

// aliasPath return the alias path, if one exists for the action.
func (handler *actionHandler) aliasPath() string {
	if handler.alias != "" {
		parts := strings.Split(strings.TrimSpace(handler.alias), " ")
		alias := ""
		if len(parts) == 1 {
			alias = parts[0]
		} else if len(parts) == 2 {
			alias = parts[1]
		} else {
			panic(fmt.Sprint("Invalid alias format! -> ", handler.alias))
		}
		return alias
	}
	return ""
}

// pattern return the path pattern used to map URL in the http.ServeMux
func (handler *actionHandler) pattern() string {
	actionPath := strings.Replace(handler.action, ".", "/", -1)
	fullPath := ""
	aliasPath := handler.aliasPath()
	if aliasPath != "" {
		fullPath = fmt.Sprint(handler.routePath, "/", aliasPath)
	} else {
		fullPath = fmt.Sprint(handler.routePath, "/", actionPath)
	}
	return strings.Replace(fullPath, "//", "/", -1)
}

var validMethods = []string{"GET", "POST", "PUT", "DELETE"}

func validMethod(method string) bool {
	for _, item := range validMethods {
		if item == method {
			return true
		}
	}
	return false
}

//acceptedMethods return a map of accepted methods for this handler.
func (handler *actionHandler) acceptedMethods() map[string]bool {
	if handler.acceptedMethodsCache != nil {
		return handler.acceptedMethodsCache
	}
	if handler.alias != "" {
		parts := strings.Split(strings.TrimSpace(handler.alias), " ")
		if len(parts) == 2 {
			method := strings.ToUpper(parts[0])
			if validMethod(method) {
				handler.acceptedMethodsCache = map[string]bool{
					method: true,
				}
				return handler.acceptedMethodsCache
			}
		}
	}
	handler.acceptedMethodsCache = map[string]bool{
		"GET":    true,
		"POST":   true,
		"PUT":    true,
		"DELETE": true,
	}
	return handler.acceptedMethodsCache
}

// invalidHttpMethodError send an error in the reponse about the http method being invalid.
func invalidHttpMethodError(logger *log.Entry, response http.ResponseWriter, methods map[string]bool) {
	acceptedMethods := []string{}
	for methodName := range methods {
		acceptedMethods = append(acceptedMethods, methodName)
	}
	error := fmt.Errorf("Invalid HTTP Method - accepted methods: %s", acceptedMethods)
	sendReponse(logger, payload.New(error), response)
}

var succesStatusCode = 200
var errorStatusCode = 500
var resultParseErrorStatusCode = 500

// sendReponse send the result payload  back using the ResponseWriter
func sendReponse(logger *log.Entry, result moleculer.Payload, response http.ResponseWriter) {
	serializer := serializer.CreateJSONSerializer(logger)
	json := serializer.PayloadToBytes(result)
	//if logger.Level == log.DebugLevel {
	logger.Debug("Gateway SendReponse() - result: ", result, " json: ", string(json))
	//}
	if result.IsError() {
		response.WriteHeader(errorStatusCode)
	} else {
		response.WriteHeader(succesStatusCode)
	}
	response.Write(json)
}

func paramsFromRequestForm(request *http.Request, logger *log.Entry) (map[string]interface{}, error) {
	params := map[string]interface{}{}
	err := request.ParseForm()
	if err != nil {
		logger.Error("Error calling request.ParseForm() -> ", err)
		return nil, err
	}
	for name, value := range request.Form {
		if len(value) == 1 {
			params[name] = value[0]
		} else {
			params[name] = value
		}
	}
	return params, nil
}

// paramsFromRequest extract params from body and URL into a payload.
func paramsFromRequest(request *http.Request, logger *log.Entry) moleculer.Payload {
	mvalues, err := paramsFromRequestForm(request, logger)
	if len(mvalues) > 0 {
		return payload.New(mvalues)
	}
	if err != nil {
		return payload.Error("Error trying to parse request form values. Error: ", err.Error())
	}
	serializer := serializer.CreateJSONSerializer(logger)
	bts, err := ioutil.ReadAll(request.Body)
	if err != nil {
		return payload.Error("Error trying to parse request body. Error: ", err.Error())
	}
	return serializer.BytesToPayload(&bts)
}

func (handler *actionHandler) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	methods := handler.acceptedMethods()
	logger := handler.context.Logger()
	switch request.Method {
	case http.MethodGet:
		if methods["GET"] {
			sendReponse(logger, <-handler.context.Call(handler.action, paramsFromRequest(request, logger)), response)
		}
	case http.MethodPost:
		if methods["POST"] {
			sendReponse(logger, <-handler.context.Call(handler.action, paramsFromRequest(request, logger)), response)
		}
	case http.MethodPut:
		if methods["PUT"] {
			sendReponse(logger, <-handler.context.Call(handler.action, paramsFromRequest(request, logger)), response)
		}
	case http.MethodDelete:
		if methods["DELETE"] {
			sendReponse(logger, <-handler.context.Call(handler.action, paramsFromRequest(request, logger)), response)
		}
	default:
		invalidHttpMethodError(logger, response, methods)
	}
}

func invertStringMap(in map[string]string) map[string]string {
	out := map[string]string{}
	for key, value := range in {
		out[value] = key
	}
	return out
}

//createActionHandlers create actionHanler for each action with the prefixPath.
func createActionHandlers(route map[string]interface{}, actions []string) []*actionHandler {
	routePath := route["path"].(string)
	mappingPolicy, exists := route["mappingPolicy"].(string)
	if !exists {
		mappingPolicy = "all"
	}
	aliases, exists := route["aliases"].(map[string]string)
	if !exists {
		aliases = map[string]string{}
	}
	actionToAlias := invertStringMap(aliases)

	result := []*actionHandler{}
	for _, action := range actions {
		actionAlias, exists := actionToAlias[action]
		if !exists && mappingPolicy == "restrict" {
			continue
		}
		result = append(result, &actionHandler{alias: actionAlias, routePath: routePath, action: action})
	}
	return result
}

// fetchServices fetch the services and actions that will be exposed.
func fetchServices(context moleculer.Context) []map[string]interface{} {
	services := <-context.Call("$node.services", map[string]interface{}{
		"onlyAvailable": true,
		"withActions":   true,
	})
	if services.IsError() {
		context.Logger().Error("Could not load the list of services/action from the register. Error: ", services.Error())
		return []map[string]interface{}{}
	}
	return services.MapArray()
}

//filterActions with a list of services collect all actions, applyfilter based on
// whitelist settings and create action handlers for each action.
func filterActions(settings map[string]interface{}, services []map[string]interface{}) []*actionHandler {
	result := []*actionHandler{}
	routes := settings["routes"].([]map[string]interface{})
	for _, route := range routes {
		filteredActions := []string{}
		_, exists := route["whitelist"]
		whitelist := []string{"**"}
		if exists {
			whitelist = route["whitelist"].([]string)
		}
		for _, service := range services {
			actions := service["actions"].([]map[string]interface{})
			for _, action := range actions {
				actionFullName := fmt.Sprint(service["name"].(string), ".", action["name"].(string))
				if shouldInclude(whitelist, actionFullName) {
					filteredActions = append(filteredActions, actionFullName)
				}
			}
		}
		for _, actionHand := range createActionHandlers(route, filteredActions) {
			result = append(result, actionHand)
		}
	}
	return result
}

//onErrorHandler default error handler.
func onErrorHandler() {
	//TODO
}

var defaultRoutes = []map[string]interface{}{
	map[string]interface{}{
		"path": "/",

		//whitelist filter used to filter the list of actions.
		//accept regex, and wildcard on action name
		//regex: /^math\.\w+$/
		//wildcard: posts.*
		"whitelist": []string{"**"},

		//mappingPolicy -> all : include all actions, the ones with aliases and without.
		//mappingPolicy -> restrict : include only actions that are in the list of aliases.
		"mappingPolicy": "all",

		//aliases -> alias names instead of action names.
		// "aliases": map[string]interface{}{
		// 	"login": "auth.login"
		// },

		//authorization turn on/off authorization
		"authorization": false,
	},
}

var defaultSettings = map[string]interface{}{

	// reverseProxy define a reverse proxy for local development and avoid CORS issues :)
	"reverseProxy": false,

	// Exposed port
	"port": "3100",

	// Exposed IP
	"ip": "0.0.0.0",

	// Used server instance. If null, it will create a new HTTP(s)(2) server
	// If false, it will start without server in middleware mode
	//"server": true,

	// Log the request ctx.params (default to "debug" level)
	"logRequestParams": "debug",

	// Log the response data (default to disable)
	"logResponseData": nil,

	// If set to true, it will log 4xx client errors, as well
	"log4XXResponses": false,

	// Use HTTP2 server (experimental)
	//"http2": false,

	// Optimize route order
	"optimizeOrder": true,

	//routes
	"routes": defaultRoutes,

	"assets": map[string]interface{}{
		"folder":  "./www",
		"options": map[string]interface{}{
			//options for static module
		},
	},

	"onError": onErrorHandler,
}

// populateActionsRouter create a new mux.router
func populateActionsRouter(context moleculer.Context, settings map[string]interface{}, router *mux.Router) {
	for _, actionHand := range filterActions(settings, fetchServices(context)) {
		actionHand.context = context
		path := actionHand.pattern()
		context.Logger().Trace("populateActionsRouter() action -> ", actionHand.action, " path: ", path)
		router.Handle(actionHand.pattern(), actionHand)
	}
}

// when enable these are the default values
var defaultReverseProxy = map[string]interface{}{
	//reserse proxy target ip:port
	"target": "http://localhost:3000",
	//reserse proxy path
	"targetPath": "/",
	//gateway endppint path
	"gatewayPath": "/api",
}

// createReverseProxy creates a reverse proxy to serve app UI content for ecample on path X and API (gateway content) on path Y.
// used mostly for development.
func createReverseProxy(context moleculer.Context, settings map[string]interface{}, instance *moleculer.Service) *mux.Router {
	gatewayPath := settings["gatewayPath"].(string)

	target := settings["target"].(string)
	targetPath := settings["targetPath"].(string)

	targetUrl, err := url.Parse(target)
	if err != nil {
		panic(errors.New(fmt.Sprint("createReverseProxy() parameter target is invalid. It must be a valid URL! - error: ", err.Error())))
	}
	targetProxy := httputil.NewSingleHostReverseProxy(targetUrl)

	routes := mux.NewRouter()

	fmt.Println("createReverseProxy() handle gatewayPath: ", gatewayPath)
	gatewayRouter := routes.PathPrefix(gatewayPath).Subrouter()
	populateActionsRouter(context, instance.Settings, gatewayRouter)

	fmt.Println("createReverseProxy() handle targetPath: ", targetPath)
	routes.PathPrefix(targetPath).Handler(targetProxy)
	return routes
}

func getAddress(instance *moleculer.Service) string {
	fmt.Println("final instanceSettings: ", instance.Settings)
	ip := instance.Settings["ip"].(string)
	port := instance.Settings["port"].(string)
	return fmt.Sprint(ip, ":", port)
}

//Service create the service schema for the API Gateway service.
func Service(settings ...map[string]interface{}) moleculer.Service {
	var server *http.Server
	mutex := &sync.Mutex{}
	var instance *moleculer.Service
	allSettings := []map[string]interface{}{defaultSettings}
	for _, set := range settings {
		if set != nil {
			allSettings = append(allSettings, set)
		}
	}

	// create the tree of handler again due some change, usualy a service being added or removed.
	resetHandlers := func(context moleculer.Context) {
		enableCors := false
		if server != nil {
			server.Shutdown(nil)
		}
		mutex.Lock()
		address := getAddress(instance)
		server = &http.Server{Addr: address}
		context.Logger().Info("Gateway starting server on: ", address)

		reverseProxy, hasReverseProxy := instance.Settings["reverseProxy"].(map[string]interface{})
		if hasReverseProxy {
			settings := service.MergeSettings(defaultReverseProxy, reverseProxy)
			context.Logger().Debug("Gateway resetHandlers() - reverse proxy enabled - settings: ", settings)
			server.Handler = createReverseProxy(context, settings, instance)
		} else {
			routes := mux.NewRouter()
			gatewayRouter := routes.PathPrefix("/").Subrouter()
			populateActionsRouter(context, instance.Settings, gatewayRouter)
			server.Handler = routes
		}
		if enableCors {
			server.Handler = cors.Default().Handler(server.Handler)
		}
		err := server.ListenAndServe()
		if err != nil && err.Error() != "http: Server closed" {
			context.Logger().Error("Error listening server on: ", address, " error: ", err)
		}
		context.Logger().Info("Server stopped -> address: ", address)
		server = nil
		mutex.Unlock()
	}

	return moleculer.Service{
		Name:         "api",
		Settings:     service.MergeSettings(allSettings...),
		Dependencies: []string{"$node"},
		Created: func(svc moleculer.Service, logger *log.Entry) {
			instance = &svc
		},
		Started: func(context moleculer.BrokerContext, svc moleculer.Service) {
			instance = &svc
			go resetHandlers(context.(moleculer.Context))
		},
		Stopped: func(context moleculer.BrokerContext, svc moleculer.Service) {
			context.Logger().Info("Gateway stopped()")
			if server == nil {
				return
			}
			err := server.Shutdown(nil)
			if err != nil {
				context.Logger().Error("Error shutting down server - error: ", err)
			}
		},
		Events: []moleculer.Event{
			moleculer.Event{
				Name: "$registry.service.added",
				Handler: func(context moleculer.Context, params moleculer.Payload) {
					go resetHandlers(context)
				},
			},
			moleculer.Event{
				Name: "$registry.service.removed",
				Handler: func(context moleculer.Context, params moleculer.Payload) {
					go resetHandlers(context)
				},
			},
		},
	}
}
