package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"encoding/json"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/cognitoidentityprovider"
)

const SECRETS_PATH = "../../.secrets.yml"

type CognitoClient struct {
	Client      *cognitoidentityprovider.Client
	AppClientId string
	UserPoolId  string
}

type Request = events.APIGatewayV2HTTPRequest
type Response = events.APIGatewayV2HTTPResponse

type Secrets struct {
	host               string `yaml:"MYSQLHOST"`
	port               string `yaml:"MYSQLPORT"`
	database           string `yaml:"MYSQLDATABASE"`
	user               string `yaml:"MYSQLUSER"`
	password           string `yaml:"MYSQLPASS"`
	region             string `yaml:"AWS_DEFAULT_REGION"`
	cognitoAppClientId string `yaml:"COGNITO_APP_CLIENT_ID"`
	cognitoUserPoolId  string `yaml:"COGNITO_USER_POOL_ID"`
}

type User struct {
	// gorm.Model
	CreatedAt      time.Time
	UpdatedAt      time.Time
	DeletedAt      time.Time `gorm:"index"`
	Username       string    `gorm:"varchar(128);primarykey"`
	Bio            string    `gorm:"varchar(1024)"`
	ProfilePicture string    `gorm:"varchar(512)"`
}

var secrets = Secrets{
	os.Getenv("MYSQLHOST"),
	os.Getenv("MYSQLPORT"),
	os.Getenv("MYSQLDATABASE"),
	os.Getenv("MYSQLUSER"),
	os.Getenv("MYSQLPASS"),
	os.Getenv("AWS_DEFAULT_REGION"),
	os.Getenv("COGNITO_APP_CLIENT_ID"),
	os.Getenv("COGNITO_USER_POOL_ID"),
}

// https://github.com/gugazimmermann/fazendadojuca/blob/master/animals/main.go

func connectDB() (*gorm.DB, error) {
	connectionString := fmt.Sprintf("%s:%s@tcp(%s:%s)/%s?allowNativePasswords=true&parseTime=true", secrets.user, secrets.password, secrets.host, secrets.port, secrets.database)
	if db, err := gorm.Open(mysql.Open(connectionString), &gorm.Config{}); err != nil {
		return nil, fmt.Errorf("error: failed to connect to AWS RDS: %w", err)
	} else {
		return db, nil
	}
}

func initClient(ctx context.Context) (*CognitoClient, error) {
	cfg, err := config.LoadDefaultConfig(
		ctx, config.WithRegion("us-east-1"),
	)
	if err != nil {
		return nil, err
	}

	return &CognitoClient{
		cognitoidentityprovider.NewFromConfig(cfg),
		secrets.cognitoAppClientId,
		secrets.cognitoUserPoolId,
	}, nil
}

func handler(ctx context.Context, req Request) (Response, error) {
	switch req.RequestContext.HTTP.Method {
	case "GET":
		return read(ctx, req)
	case "PUT":
		return update(req)
	default:
		err := fmt.Errorf("HTTP method '%s' not allowed", req.RequestContext.HTTP.Method)
		return Response{StatusCode: 405, Body: err.Error()}, nil
	}
}

// GET: Returns user info
func read(ctx context.Context, req Request) (Response, error) {
	db, err := connectDB()
	if err != nil {
		return Response{StatusCode: 500, Body: err.Error()}, nil
	}

	username, ok := req.RequestContext.Authorizer.Lambda["username"].(string)
	if !ok {
		return Response{StatusCode: 500, Body: "failed to parse username"}, nil
	}

	cognitoClient, err := initClient(ctx)
	if err != nil {
		return Response{StatusCode: 500, Body: err.Error()}, nil
	}

	authToken := strings.Split((req.Headers["authorization"]), " ")[1]

	userIn := cognitoidentityprovider.GetUserInput{
		AccessToken: &authToken,
	}

	cogInfo, err := cognitoClient.Client.GetUser(ctx, &userIn)
	if err != nil {
		return Response{StatusCode: 500, Body: err.Error()}, nil
	}

	// get email from user attributes
	email := ""
	nickname := ""
	for _, v := range cogInfo.UserAttributes {
		if *v.Name == "email" {
			email = *v.Value
		} else if *v.Name == "nickname" {
			nickname = *v.Value
		}
	}
	if len(email) == 0 {
		return Response{StatusCode: 500, Body: "could not find user email"}, nil
	}

	// find and get user info from db
	var user User
	result := db.Where("username = ?", username).First(&user)
	if result.Error != nil {
		return Response{StatusCode: 404, Body: "user not found"}, nil
	}

	// Combines the two JSON's to one string
	var buf bytes.Buffer
	response, err := json.Marshal(map[string]interface{}{
		"username":       username,
		"nickname":       nickname,
		"email":          email,
		"bio":            user.Bio,
		"profilePicture": user.ProfilePicture,
	})
	if err != nil {
		return Response{StatusCode: 500, Body: "could not marshal JSON"}, nil
	}
	json.HTMLEscape(&buf, response)

	return Response{
		StatusCode: 200,
		Body:       buf.String(),
		Headers: map[string]string{
			"Content-Type": "application/json",
		},
	}, nil
}

func update(req Request) (Response, error) {
	db, err := connectDB()
	if err != nil {
		return Response{StatusCode: 500, Body: err.Error()}, err
	}

	username, ok := req.RequestContext.Authorizer.Lambda["username"].(string)
	if !ok {
		return Response{StatusCode: 500, Body: "failed to parse username"}, nil
	}

	var user User
	result := db.Where("username = ?", username).First(&user)
	if result.Error != nil {
		return Response{StatusCode: 404, Body: "user not found"}, nil
	}

	// put changes into new user struct
	err = json.Unmarshal([]byte(req.Body), &user)
	if err != nil {
		return Response{StatusCode: 400, Body: "invalid request body"}, nil
	}

	// update user in the database
	updatedUser := db.Save(&user)
	if updatedUser.Error != nil {
		return Response{StatusCode: 500, Body: updatedUser.Error.Error()}, nil
	}

	return Response{StatusCode: 200, Body: "user updated successfully"}, nil
}

func main() {
	lambda.Start(handler)
}
