package main

/*
  This is close variation of jbarratt@ repo here
  https://github.com/jbarratt/envoy_ratelimit_example/blob/master/extauth/main.go

*/
import (
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"strings"
	"time"

	"golang.org/x/net/context"
	"google.golang.org/grpc"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"

	rpcstatus "google.golang.org/genproto/googleapis/rpc/status"

	core "github.com/envoyproxy/go-control-plane/envoy/api/v2/core"
	auth "github.com/envoyproxy/go-control-plane/envoy/service/auth/v2"
	envoy_type "github.com/envoyproxy/go-control-plane/envoy/type"
	"github.com/gogo/googleapis/google/rpc"

	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"io/ioutil"

	jwt "github.com/dgrijalva/jwt-go"
	"github.com/golang/glog"
)

var (
	grpcport            = flag.String("grpcport", ":50051", "grpcport")
	conn                *grpc.ClientConn
	hs                  *health.Server
	AUTHZ_ALLOWED_USERS = os.Getenv("AUTHZ_ALLOWED_USERS")

	AUTHZ_ISSUER        = os.Getenv("AUTHZ_ISSUER")
	AUTHZ_SERVER_KEY_ID = os.Getenv("AUTHZ_SERVER_KEY_ID")
	privateKey          *rsa.PrivateKey
)

func stringInSlice(a string, list []string) bool {
	for _, b := range list {
		if b == a {
			return true
		}
	}
	return false
}

const (
	address string = ":50051"
	keyfile string = "/data/certs/key.pem"
)

type MyCustomClaims struct {
	Uid string   `json:"uid"`
	Groups []string `json:"groups"` // https://github.com/dgrijalva/jwt-go/pull/308
	jwt.StandardClaims
}

type healthServer struct{}

func (s *healthServer) Check(ctx context.Context, in *healthpb.HealthCheckRequest) (*healthpb.HealthCheckResponse, error) {
	/*Perform all readiness tests here. e.g. connetcing to LDAP, database, Key service, couchbase policy store etc. If all checks pass, return HealthCheckResponse_SERVING, otherwise return HealthCheckResponse_NOT_SERVING
   */
	log.Printf("grpc healthcheck request")
	return &healthpb.HealthCheckResponse{Status: healthpb.HealthCheckResponse_SERVING}, nil
}

func (s *healthServer) Watch(in *healthpb.HealthCheckRequest, srv healthpb.Health_WatchServer) error {
	log.Printf("grpc watch request")
	return status.Error(codes.Unimplemented, "Watch is not implemented")
}

type AuthorizationServer struct{}

func returnUnAuthenticated(message string) *auth.CheckResponse {
	return &auth.CheckResponse{
		Status: &rpcstatus.Status{
			Code: int32(rpc.UNAUTHENTICATED),
		},
		HttpResponse: &auth.CheckResponse_DeniedResponse{
			DeniedResponse: &auth.DeniedHttpResponse{
				Status: &envoy_type.HttpStatus{
					Code: envoy_type.StatusCode_Forbidden,
				},
				Body: message,
			},
		},
	}
}

func returnPermissionDenied(message string) *auth.CheckResponse {
	return &auth.CheckResponse{
		Status: &rpcstatus.Status{
			Code: int32(rpc.PERMISSION_DENIED),
		},
		HttpResponse: &auth.CheckResponse_DeniedResponse{
			DeniedResponse: &auth.DeniedHttpResponse{
				Status: &envoy_type.HttpStatus{
					Code: envoy_type.StatusCode_Unauthorized,
				},
				Body: message,
			},
		},
	}
}

func (a *AuthorizationServer) Check(ctx context.Context, req *auth.CheckRequest) (*auth.CheckResponse, error) {
	log.Println(">>> Authorization called check()")

	authHeader, ok := req.Attributes.Request.Http.Headers["authorization"]
	if !ok {
		return returnUnAuthenticated("Unable to find Authorization Header"), nil
	}
	var splitToken []string
	log.Printf("Authorization Header %s", authHeader)

	if ok {
		splitToken = strings.Split(authHeader, "Bearer ")
	} else {
		return returnUnAuthenticated("Unable to parse Authorization Header"), nil
	}
	if len(splitToken) == 2 {
		token := splitToken[1]

		if stringInSlice(token, strings.Split(AUTHZ_ALLOWED_USERS, ",")) {

			var groups []string
			if token == "user1" {
				groups = []string{"svc1-group"}
			} else if token == "user2" {
				groups = []string{"svc2-group"}
			} else if token == "user3" {
				groups = []string{"svc3-group"}
			} else {
				groups = []string{}
			}
			claims := MyCustomClaims{
				token,
				groups,
				jwt.StandardClaims{
					Issuer:  AUTHZ_ISSUER,
					Subject: AUTHZ_ISSUER,
					//Audience:  aud,
					IssuedAt:  time.Now().Unix(),
					ExpiresAt: time.Now().Add(time.Minute * 1).Unix(),
				},
			}

			token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
			token.Header["kid"] = AUTHZ_SERVER_KEY_ID
			ss, err := token.SignedString(privateKey)
			if err != nil {
				return returnUnAuthenticated("Unable to generate JWT"), nil
			}

			log.Printf("Issuing outbound Header %s", ss)

			return &auth.CheckResponse{
				Status: &rpcstatus.Status{
					Code: int32(rpc.OK),
				},
				HttpResponse: &auth.CheckResponse_OkResponse{
					OkResponse: &auth.OkHttpResponse{
						Headers: []*core.HeaderValueOption{
							{
								Header: &core.HeaderValue{
									Key:   "Authorization",
									Value: "Bearer " + ss,
								},
							},
						},
					},
				},
			}, nil
		} else {
			return returnPermissionDenied("Permission Denied"), nil

		}

	}
	return returnUnAuthenticated("Authorization header not provided"), nil
}

func main() {

	flag.Parse()

	if *grpcport == "" {
		fmt.Fprintln(os.Stderr, "missing -grpcport flag (:50051)")
		flag.Usage()
		os.Exit(2)
	}

	lis, err := net.Listen("tcp", *grpcport)
	if err != nil {
		log.Fatalf("failed to listen: %v", err)
	}

	opts := []grpc.ServerOption{grpc.MaxConcurrentStreams(10)}
	opts = append(opts)

	s := grpc.NewServer(opts...)

	auth.RegisterAuthorizationServer(s, &AuthorizationServer{})
	healthpb.RegisterHealthServer(s, &healthServer{})

	data, err := ioutil.ReadFile(keyfile)
	if err != nil {
		glog.Fatal(err)
	}

	block, _ := pem.Decode(data)

	privateKey, err = x509.ParsePKCS1PrivateKey(block.Bytes)
	if err != nil {
		glog.Fatal(err)
	}

	log.Printf("Starting gRPC Server at %s", *grpcport)
	s.Serve(lis)

}
