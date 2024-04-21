package opentelemetryexporter

import (
	"context"
	"log"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	semconv "go.opentelemetry.io/otel/semconv/v1.17.0"
	"google.golang.org/grpc"
	"google.golang.org/grpc/backoff"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/stretchr/testify/assert"
	"github.com/thomaspoignant/go-feature-flag/exporter"
)

type testSubStruct struct {
	SubContent      string
	SubTimeStamp    int64
	SubCondition    bool
	SubValue        float32
	SubAnotherValue float64
	subNotExported  bool
}

type testStruct struct {
	Substruct    testSubStruct
	Content      string
	Timestamp    int64
	Condition    bool
	Value        float32
	AnotherValue float64
	notExported  bool
}

func checkIfGithubActionCI() bool {
	// https://docs.github.com/en/actions/learn-github-actions/variables#default-environment-variables
	_, ok1 := os.LookupEnv("CI")
	_, ok2 := os.LookupEnv("GITHUB_RUN_ID")
	return ok1 && ok2
}

func buildFeatureEvents() []exporter.FeatureEvent {
	return []exporter.FeatureEvent{
		{
			Kind: "feature", ContextKind: "anonymousUser", UserKey: "ABCD", CreationDate: 1617970547, Key: "random-key",
			Variation: "Default", Value: "YO", Default: false,
		},
		{
			Kind: "feature", ContextKind: "anonymousUser", UserKey: "ABCDEF", CreationDate: 1617970547, Key: "random-key",
			Variation: "Default", Value: "YO", Default: false,
		},
		{
			Kind: "feature", ContextKind: "anonymousUser", UserKey: "ABCDEF", CreationDate: 1617970547, Key: "random-key",
			Variation: "Default", Value: testStruct{
				Timestamp: 192929922, Condition: true, Content: "hello", notExported: false, Value: 1.0, AnotherValue: 3.3,
				Substruct: testSubStruct{SubCondition: false, SubContent: "world", SubValue: 3.0, SubAnotherValue: 44.4, subNotExported: true},
			}, Default: false,
		},
	}
}

func getSpanStubs(target string) tracetest.SpanStubs {
	s := make(tracetest.SpanStubs, 0)
	s = append(s, tracetest.SpanStub{Name: target, StartTime: time.Now()})
	return s
}

func assertResource(t *testing.T, expected resource.Resource, actual resource.Resource) {
	var found bool
	for _, target := range expected.Attributes() {
		for _, attr := range actual.Attributes() {
			if target.Key == attr.Key && target.Value == attr.Value {
				found = true
			}
		}
		assert.True(t, found)
	}
}

func TestCI(t *testing.T) {
	t.Setenv("GITHUB_RUN_ID", "does-not-matter")
	t.Setenv("CI", "true")
	assert.True(t, checkIfGithubActionCI())
}

func TestValueReflection(t *testing.T) {
	v := valueToAttributes("foo", "value", 2, 0)
	assert.Len(t, v, 1)
	assert.Len(t, valueToAttributes(1, "value", 2, 0), 1)
	assert.Len(t, valueToAttributes(true, "value", 2, 0), 1)
	assert.Len(t, valueToAttributes(3.2, "value", 2, 0), 1)

	testStruct := testStruct{
		Timestamp: 192929922, Condition: true, Content: "hello", notExported: false, Value: 1.0, AnotherValue: 3.3,
		Substruct: testSubStruct{SubCondition: false, SubContent: "world", SubValue: 3.0, SubAnotherValue: 44.4, subNotExported: true},
	}

	prefix := "value"

	event := exporter.FeatureEvent{Value: testStruct}
	structAttrs := valueToAttributes(event.Value, prefix, 2, 0)
	assert.Len(t, structAttrs, 10)
	for _, attr := range structAttrs {
		assert.True(t, strings.HasPrefix(string(attr.Key), prefix+"."))
	}
}

func TestFeatureEventsToAttributes(t *testing.T) {
	// TODO: Build Various kinds of events
	featureEvents := buildFeatureEvents()

	for _, featureEvent := range featureEvents {
		attributes := featureEventToAttributes(featureEvent)
		assert.True(t, len(attributes) == 10 || len(attributes) == 19)
	}
}

func TestResource(t *testing.T) {
	resource := defaultResource()
	assert.NotNil(t, resource)
	assert.NotNil(t, resource.SchemaURL())

	attributes := resource.Attributes()
	assert.Len(t, attributes, 2)
}

func TestExporterBuildsWithOptions(t *testing.T) {
	userCustomResource := resource.NewWithAttributes(
		semconv.SchemaURL, attribute.KeyValue{Key: "hello", Value: attribute.StringValue("World")})

	inMemoryExporter := PersistentInMemoryExporter{}
	inMemoryProcessor := sdktrace.NewBatchSpanProcessor(&inMemoryExporter)
	exporter, err := NewExporter(

		WithResource(userCustomResource),
		WithBatchSpanProcessors(&inMemoryProcessor),
	)
	assert.NoError(t, err)
	assert.NotNil(t, exporter)
	assert.NotNil(t, exporter.resource)
	assert.Len(t, exporter.resource.Attributes(), 3)
	// Check that our default resource wins the merge
	assertResource(t, *defaultResource(), *exporter.resource)
	// Check we didn't step on the users resource
	assertResource(t, *userCustomResource, *exporter.resource)
	assert.Len(t, exporter.processors, 1)
}

func TestInitProviderRequiresProcessor(t *testing.T) {
	_, err := initProvider(&Exporter{})
	assert.NotNil(t, err)
}

func TestPersistentInMemoryExporter(t *testing.T) {
	ctx := context.Background()

	inMemorySpanExporter := PersistentInMemoryExporter{}

	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sdktrace.NewBatchSpanProcessor(&inMemorySpanExporter)))
	tracer := tp.Tracer("tracer")
	_, span := tracer.Start(ctx, "span")
	span.End()

	err := tp.ForceFlush(ctx)
	assert.NoError(t, err)

	assert.Len(t, inMemorySpanExporter.GetSpans(), 1)
	err = inMemorySpanExporter.Shutdown(ctx)
	assert.NoError(t, err)
	assert.Len(t, inMemorySpanExporter.GetSpans(), 1)
	inMemorySpanExporter.Reset()
	assert.Len(t, inMemorySpanExporter.GetSpans(), 0)
}

func TestExportWithMultipleProcessors(t *testing.T) {
	featureEvents := buildFeatureEvents()

	ctx := context.Background()
	logger := log.New(os.Stdout, "", 0)

	inMemoryExporter := PersistentInMemoryExporter{}
	inMemoryProcessor := sdktrace.NewBatchSpanProcessor(&inMemoryExporter)
	stdoutProcessor, err := stdoutBatchSpanProcessor()
	assert.NoError(t, err)
	resource := defaultResource()

	exp, err := NewExporter(

		WithResource(resource),
		WithBatchSpanProcessors(&inMemoryProcessor, &stdoutProcessor),
	)
	assert.NoError(t, err)
	err = exp.Export(ctx, logger, featureEvents)
	assert.NoError(t, err)
	//  We sent three spans, the parents and three child spans corresponding to events
	assert.Len(t, inMemoryExporter.GetSpans(), 4)
	for _, span := range inMemoryExporter.GetSpans() {
		assert.NotNil(t, span)
	}

	// Test the referential integrity of the spans
	for _, span := range inMemoryExporter.GetSpans() {
		if span.Parent.HasTraceID() {
			assert.Equal(t, span.Parent.TraceID(), span.SpanContext.TraceID())
			assert.NotEqual(t, span.Parent.SpanID(), span.SpanContext.SpanID())
			assert.Equal(t, span.ChildSpanCount, 0)
		} else {
			assert.Equal(t, span.ChildSpanCount, 3)
		}
		assert.NotNil(t, span.Resource)

		if span.Parent.HasTraceID() {
			assert.NotNil(t, span.Attributes)
			// Different spans have different attributes
			assert.GreaterOrEqual(t, len(span.Attributes), 1)
		}
	}
}

func TestOtelBSPNeedsOptions(t *testing.T) {
	_, err := OtelCollectorBatchSpanProcessor("localhost")
	assert.NotNil(t, err)
}

func TestOtelExporterDirectly(t *testing.T) {
	ctx := context.Background()

	consumer := AppendingLogConsumer{}
	otelC, err := setupOtelCollectorContainer(ctx, &consumer)
	assert.NoError(t, err)

	connectParams := grpc.ConnectParams{
		Backoff: backoff.Config{BaseDelay: time.Second * 2,
			Multiplier: 2.0,
			MaxDelay:   time.Second * 16}}
	otelExporter, err := otelExporter(otelC.URI,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithConnectParams(connectParams))

	assert.NoError(t, err)

	target := "test-go-feature-flag-export"

	spans := getSpanStubs(target)
	err = otelExporter.ExportSpans(ctx, spans.Snapshots())
	assert.NoError(t, err)

	time.Sleep(10 * time.Second)
	consumer.Display()
	assert.True(t, consumer.Exists(target))

	assert.NoError(t, err)
}

func TestExportToOtelCollector(t *testing.T) {
	containerWaitTime := time.Second * 5

	if checkIfGithubActionCI() {
		log.Println("Setting timeout for CI")
		containerWaitTime = time.Second * 20
	}

	featureEvents := buildFeatureEvents()

	ctx := context.Background()
	logger := log.New(os.Stdout, "", 0)

	consumer := AppendingLogConsumer{}
	otelC, err := setupOtelCollectorContainer(ctx, &consumer)
	assert.NoError(t, err)

	connectParams := grpc.ConnectParams{
		Backoff: backoff.Config{BaseDelay: time.Second * 2,
			Multiplier: 2.0,
			MaxDelay:   time.Second * 16}}
	otelProcessor, err := OtelCollectorBatchSpanProcessor(otelC.URI,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithConnectParams(connectParams))
	assert.NoError(t, err)
	resource := defaultResource()

	exp, err := NewExporter(
		WithResource(resource),
		WithBatchSpanProcessors(&otelProcessor),
	)
	assert.NoError(t, err)
	err = exp.Export(ctx, logger, featureEvents)
	assert.NoError(t, err)
	// Sleep to give the container time to process the spans
	time.Sleep(containerWaitTime)
	assert.GreaterOrEqual(t, consumer.Size(), 1)
	// Remove. I need to see what is making it in CI
	if checkIfGithubActionCI() || true {
		consumer.Display()
		dial := strings.Replace(otelC.URI, "/", ":", 1)
		conn, err := net.Dial("tcp", dial)
		assert.NoError(t, err)
		defer conn.Close()
	}
	assert.True(t, consumer.Exists(instrumentationName))

	// Clean up the container after the test is complete
	t.Cleanup(func() {
		if err := otelC.Terminate(ctx); err != nil {
			t.Fatalf("failed to terminate container: %s", err)
		}
	})
}
