package main

import (
	"context"
	"net/http"
	"os"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.opentelemetry.io/contrib/instrumentation/github.com/gin-gonic/gin/otelgin"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.17.0"
	"google.golang.org/grpc"
)

type User struct {
	ID    primitive.ObjectID `bson:"_id,omitempty" json:"id,omitempty"`
	Name  string             `bson:"name" json:"name"`
	Email string             `bson:"email" json:"email"`
}

var collection *mongo.Collection
var tracer = otel.Tracer("gin-mongo-example")

func setupLogging() {
	// Multi-writer for both console and file
	consoleWriter := zerolog.ConsoleWriter{Out: os.Stdout, TimeFormat: time.RFC3339}

	// Open a file for logging
	fileWriter, err := os.OpenFile("app.log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to open log file")
	}

	multi := zerolog.MultiLevelWriter(consoleWriter, fileWriter)

	log.Logger = zerolog.New(multi).With().Timestamp().Caller().Logger()

	// Set global log level
	zerolog.SetGlobalLevel(zerolog.InfoLevel)

	// Enable caller tracking
	log.Logger = log.With().Caller().Logger()
}

func main() {
	setupLogging()
	// Initialize zerolog
	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stderr})

	// Initialize the tracer
	cleanup := initTracer()
	defer cleanup()

	// Connect to MongoDB
	client, err := mongo.Connect(context.Background(), options.Client().ApplyURI("mongodb://root:root@localhost:27017/testdb?authSource=admin"))
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to connect to MongoDB")
	}
	defer client.Disconnect(context.Background())

	collection = client.Database("testdb").Collection("users")

	// Initialize Gin
	r := gin.New()
	r.Use(gin.Recovery())
	r.Use(otelgin.Middleware("my-server"))

	// Routes
	r.POST("/users", createUser)
	r.GET("/users/:id", getUser)
	r.PUT("/users/:id", updateUser)
	r.DELETE("/users/:id", deleteUser)

	// Start server
	if err := r.Run(":8080"); err != nil {
		log.Fatal().Err(err).Msg("Failed to start server")
	}
}

func initTracer() func() {
	exporter, err := otlptracegrpc.New(context.Background(),
		otlptracegrpc.WithInsecure(),
		otlptracegrpc.WithEndpoint("localhost:4317"),
		otlptracegrpc.WithDialOption(grpc.WithBlock()))
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to create exporter")
	}

	resources, err := resource.New(
		context.Background(),
		resource.WithAttributes(
			semconv.ServiceNameKey.String("gin-mongo-service"),
			semconv.ServiceVersionKey.String("1.0.0"),
		),
	)
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to create resource")
	}

	provider := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(resources),
	)

	otel.SetTracerProvider(provider)

	return func() {
		if err := provider.Shutdown(context.Background()); err != nil {
			log.Error().Err(err).Msg("Failed to shutdown TracerProvider")
		}
	}
}

func createUser(c *gin.Context) {
	ctx, span := tracer.Start(c.Request.Context(), "createUser")
	defer span.End()

	var user User
	if err := c.ShouldBindJSON(&user); err != nil {
		log.Ctx(ctx).Error().Err(err).Msg("Failed to bind JSON")
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	result, err := collection.InsertOne(ctx, user)
	if err != nil {
		log.Ctx(ctx).Error().Err(err).Msg("Failed to insert user")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create user"})
		return
	}

	user.ID = result.InsertedID.(primitive.ObjectID)
	span.SetAttributes(attribute.String("user.id", user.ID.Hex()))

	log.Ctx(ctx).Info().Str("userId", user.ID.Hex()).Msg("User created")
	c.JSON(http.StatusCreated, user)
}

func getUser(c *gin.Context) {
	ctx, span := tracer.Start(c.Request.Context(), "getUser")
	defer span.End()

	id, err := primitive.ObjectIDFromHex(c.Param("id"))
	if err != nil {
		log.Ctx(ctx).Error().Err(err).Msg("Invalid user ID")
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid user ID"})
		return
	}

	span.SetAttributes(attribute.String("user.id", id.Hex()))

	var user User
	err = collection.FindOne(ctx, bson.M{"_id": id}).Decode(&user)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			log.Ctx(ctx).Warn().Str("userId", id.Hex()).Msg("User not found")
			c.JSON(http.StatusNotFound, gin.H{"error": "User not found"})
		} else {
			log.Ctx(ctx).Error().Err(err).Str("userId", id.Hex()).Msg("Failed to get user")
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to get user"})
		}
		return
	}

	log.Ctx(ctx).Info().Str("userId", id.Hex()).Msg("User retrieved")
	c.JSON(http.StatusOK, user)
}

func updateUser(c *gin.Context) {
	ctx, span := tracer.Start(c.Request.Context(), "updateUser")
	defer span.End()

	id, err := primitive.ObjectIDFromHex(c.Param("id"))
	if err != nil {
		log.Ctx(ctx).Error().Err(err).Msg("Invalid user ID")
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid user ID"})
		return
	}

	span.SetAttributes(attribute.String("user.id", id.Hex()))

	var user User
	if err := c.ShouldBindJSON(&user); err != nil {
		log.Ctx(ctx).Error().Err(err).Msg("Failed to bind JSON")
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	update := bson.M{
		"$set": bson.M{
			"name":  user.Name,
			"email": user.Email,
		},
	}

	result, err := collection.UpdateOne(ctx, bson.M{"_id": id}, update)
	if err != nil {
		log.Ctx(ctx).Error().Err(err).Str("userId", id.Hex()).Msg("Failed to update user")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update user"})
		return
	}

	if result.MatchedCount == 0 {
		log.Ctx(ctx).Warn().Str("userId", id.Hex()).Msg("User not found")
		c.JSON(http.StatusNotFound, gin.H{"error": "User not found"})
		return
	}

	log.Ctx(ctx).Info().Str("userId", id.Hex()).Msg("User updated")
	c.JSON(http.StatusOK, gin.H{"message": "User updated successfully"})
}

func deleteUser(c *gin.Context) {
	ctx, span := tracer.Start(c.Request.Context(), "deleteUser")
	defer span.End()

	id, err := primitive.ObjectIDFromHex(c.Param("id"))
	if err != nil {
		log.Ctx(ctx).Error().Err(err).Msg("Invalid user ID")
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid user ID"})
		return
	}

	span.SetAttributes(attribute.String("user.id", id.Hex()))

	result, err := collection.DeleteOne(ctx, bson.M{"_id": id})
	if err != nil {
		log.Ctx(ctx).Error().Err(err).Str("userId", id.Hex()).Msg("Failed to delete user")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete user"})
		return
	}

	if result.DeletedCount == 0 {
		log.Ctx(ctx).Warn().Str("userId", id.Hex()).Msg("User not found")
		c.JSON(http.StatusNotFound, gin.H{"error": "User not found"})
		return
	}

	log.Ctx(ctx).Info().Str("userId", id.Hex()).Msg("User deleted")
	c.JSON(http.StatusOK, gin.H{"message": "User deleted successfully"})
}
