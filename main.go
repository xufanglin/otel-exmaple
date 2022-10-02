package main

import (
	"context"
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"os"
	"time"

	"go.opentelemetry.io/contrib/detectors/aws/ec2"
	"go.opentelemetry.io/contrib/detectors/aws/eks"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/contrib/propagators/aws/xray"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.4.0"
	"go.opentelemetry.io/otel/trace"
)

var (
	OTLP_ENDPOINT_GRPC = "0.0.0.0:4317" // 定义默认OTLP Exporter的Endpoint
	SERVICE_NAME       = "OTELDemo"
	REGION             = "ap-northeast-2"
	TR                 trace.Tracer
)

func main() {
	ctx := context.Background()        // 初始化context，用于传递可观测信号上下文传递
	traceStop := InitOTELProvider(ctx) // 初始化meter和tracer的provider，并在main函数关闭时停止trace
	defer func() {
		if err := traceStop(ctx); err != nil {
			log.Fatal(err)
		}
	}()
	// otelhttp.NewHandler在
	http.Handle("/hello", otelhttp.NewHandler(http.HandlerFunc(helloHandler), "hello"))
	http.Handle("/err", otelhttp.NewHandler(http.HandlerFunc(errHandler), "err"))
	http.Handle("/notfound", otelhttp.NewHandler(http.HandlerFunc(notfoundHandler), "notfound"))
	log.Fatal(http.ListenAndServe(":4000", nil))
}

func helloHandler(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	n := rand.New(rand.NewSource(time.Now().UnixNano())).Intn(200)
	fib, _ := fibonacci(ctx, uint(n))
	w.Write([]byte(fmt.Sprintf("Number: %d Fib: %d\n", n, fib)))
}

func errHandler(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusServiceUnavailable)
}

func notfoundHandler(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusNotFound)
}

// Fibonacci returns the n-th fibonacci number.
func fibonacci(ctx context.Context, n uint) (uint64, error) {
	_, span := TR.Start(ctx, "Fibonacci")
	defer span.End()

	if n <= 1 {
		return uint64(n), nil
	}
	// 当传输的数字超过93，结果超过unit64的表示范围，将span标记为codes.Error,并在SetAttributes记录关键信息
	if n > 93 {
		span.SetStatus(codes.Error, fmt.Sprintf("unsupported fibonacci number %d: too large", n))
		span.SetAttributes(attribute.Int("num", int(n)))
		return 0, fmt.Errorf("unsupported fibonacci number %d: too large", n)
	}

	var n2, n1 uint64 = 0, 1
	for i := uint(2); i < n; i++ {
		n2, n1 = n1, n1+n2
	}

	span.SetAttributes(attribute.Int("num", int(n)))

	return n2 + n1, nil
}

// 将Traces与底层基础设置关联，如EKS Pod ID、EC2实例ID等
func NewResource(ctx context.Context) *resource.Resource {
	// 如果未设置RESOURCE_TYPE环境变量，则使用默认值
	resType := os.Getenv("RESOURCE_TYPE")
	switch resType {
	case "EC2":
		r, err := ec2.NewResourceDetector().Detect(ctx)
		if err != nil {
			log.Fatalf("%s: %v", "Failed to detect EC2 resource", err)
		}
		res, err := resource.Merge(r, resource.NewSchemaless(semconv.ServiceNameKey.String(SERVICE_NAME)))
		if err != nil {
			log.Fatalf("%s: %v", "Failed to merge resources", err)
		}
		return res

	case "EKS": // EKS Resource的实现依赖Container Insight
		r, err := eks.NewResourceDetector().Detect(ctx)
		if err != nil {
			log.Fatalf("%s: %v", "failed to detect EKS resource", err)
		}
		res, err := resource.Merge(r, resource.NewSchemaless(semconv.ServiceNameKey.String(SERVICE_NAME)))
		if err != nil {
			log.Fatalf("%s: %v", "Failed to merge resources", err)
		}
		return res

	default:
		res := resource.NewWithAttributes(
			semconv.SchemaURL,
			// ServiceName用于在后端中标识应用
			semconv.ServiceNameKey.String(SERVICE_NAME),
		)
		return res
	}
}

// InitOTELProvider 初始化TraceProvider，返回Tracer的关闭函数
func InitOTELProvider(ctx context.Context) (traceStop func(context.Context) error) {

	ep := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")
	if ep != "" {
		OTLP_ENDPOINT_GRPC = ep // 如果设置了环境变量，则使用环境变量的值来设置exporter的endpoint
	}

	res := NewResource(ctx)

	// 初始化TracerProvider，使用grpc与collector通讯
	traceExporter, err := otlptracegrpc.New(ctx, otlptracegrpc.WithInsecure(), otlptracegrpc.WithEndpoint(OTLP_ENDPOINT_GRPC))
	if err != nil {
		log.Fatalf("%s: %v", "failed to create trace exporter", err)
	}

	idg := xray.NewIDGenerator() // x-ray的trace id包含时间戳

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSampler(sdktrace.AlwaysSample()), // 设置采样率
		sdktrace.WithBatcher(traceExporter),
		sdktrace.WithIDGenerator(idg), //使用x-ray兼容的trace id
		sdktrace.WithResource(res),
	)
	otel.SetTracerProvider(tp)

	TR = tp.Tracer(SERVICE_NAME)

	return tp.Shutdown
}
