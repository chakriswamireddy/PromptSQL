package io.governance.calcite.grpc;

import com.fasterxml.jackson.databind.ObjectMapper;
import io.governance.calcite.engine.RewriteEngine;
import io.governance.calcite.model.RewriteRequest;
import io.governance.calcite.model.RewriteResponse;
import io.grpc.stub.StreamObserver;
import io.micrometer.core.instrument.MeterRegistry;
import io.micrometer.core.instrument.Timer;
import net.devh.boot.grpc.server.service.GrpcService;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;

import java.io.IOException;

/**
 * gRPC service implementation for the CalciteRewriter service.
 *
 * Requests and responses are JSON-encoded (matching the Go client's
 * jsonRawMsg encoding). This avoids proto code generation in the Java
 * project for V1; switch to binary proto in Phase 11.
 *
 * gRPC method: /calcite.v1.CalciteRewriter/Rewrite
 */
@GrpcService
public class CalciteRewriterServiceImpl
    extends CalciteRewriterGrpc.CalciteRewriterImplBase {

    private static final Logger log = LoggerFactory.getLogger(CalciteRewriterServiceImpl.class);

    private final RewriteEngine rewriteEngine;
    private final ObjectMapper objectMapper;
    private final Timer rewriteTimer;

    public CalciteRewriterServiceImpl(
        RewriteEngine rewriteEngine,
        ObjectMapper objectMapper,
        MeterRegistry meterRegistry
    ) {
        this.rewriteEngine   = rewriteEngine;
        this.objectMapper    = objectMapper;
        this.rewriteTimer    = Timer.builder("calcite.rewrite.duration")
            .description("Time to parse + rewrite SQL")
            .register(meterRegistry);
    }

    @Override
    public void rewrite(
        com.google.protobuf.BytesValue request,
        StreamObserver<com.google.protobuf.BytesValue> responseObserver
    ) {
        rewriteTimer.record(() -> {
            try {
                // Deserialise JSON request.
                RewriteRequest req = objectMapper.readValue(
                    request.getValue().toByteArray(),
                    RewriteRequest.class
                );

                log.debug("rewrite tenant={} user={} requestId={}",
                    req.getTenantId(), req.getUserId(), req.getRequestId());

                // Run rewrite engine.
                RewriteResponse resp = rewriteEngine.rewrite(req);

                // Serialise JSON response.
                byte[] responseBytes = objectMapper.writeValueAsBytes(resp);
                responseObserver.onNext(
                    com.google.protobuf.BytesValue.of(
                        com.google.protobuf.ByteString.copyFrom(responseBytes)
                    )
                );
                responseObserver.onCompleted();

            } catch (IOException e) {
                log.error("JSON serialisation error: {}", e.getMessage());
                responseObserver.onError(
                    io.grpc.Status.INTERNAL
                        .withDescription("serialisation error")
                        .asRuntimeException()
                );
            } catch (Exception e) {
                log.error("Unexpected rewrite error: {}", e.getMessage(), e);
                responseObserver.onError(
                    io.grpc.Status.INTERNAL
                        .withDescription("internal rewrite error")
                        .asRuntimeException()
                );
            }
        });
    }
}
