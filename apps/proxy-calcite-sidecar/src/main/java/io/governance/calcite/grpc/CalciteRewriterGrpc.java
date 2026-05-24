package io.governance.calcite.grpc;

import io.grpc.MethodDescriptor;
import io.grpc.ServiceDescriptor;
import io.grpc.stub.AbstractStub;
import io.grpc.stub.StreamObserver;
import io.grpc.protobuf.ProtoUtils;
import com.google.protobuf.BytesValue;

import static io.grpc.MethodDescriptor.generateFullMethodName;
import static io.grpc.stub.ClientCalls.blockingUnaryCall;
import static io.grpc.stub.ServerCalls.asyncUnaryCall;

/**
 * Generated-style gRPC stub for CalciteRewriter service.
 *
 * Uses BytesValue to carry JSON payloads — avoids proto code generation
 * for the Rewrite request/response types in V1.
 * Switch to typed proto messages in Phase 11.
 */
public final class CalciteRewriterGrpc {

    private CalciteRewriterGrpc() {}

    public static final String SERVICE_NAME = "calcite.v1.CalciteRewriter";

    private static final MethodDescriptor<BytesValue, BytesValue> REWRITE_METHOD =
        MethodDescriptor.<BytesValue, BytesValue>newBuilder()
            .setType(MethodDescriptor.MethodType.UNARY)
            .setFullMethodName(generateFullMethodName(SERVICE_NAME, "Rewrite"))
            .setRequestMarshaller(ProtoUtils.marshaller(BytesValue.getDefaultInstance()))
            .setResponseMarshaller(ProtoUtils.marshaller(BytesValue.getDefaultInstance()))
            .build();

    private static volatile ServiceDescriptor serviceDescriptor;

    public static ServiceDescriptor getServiceDescriptor() {
        ServiceDescriptor result = serviceDescriptor;
        if (result == null) {
            synchronized (CalciteRewriterGrpc.class) {
                result = serviceDescriptor;
                if (result == null) {
                    serviceDescriptor = result = ServiceDescriptor.newBuilder(SERVICE_NAME)
                        .addMethod(REWRITE_METHOD)
                        .build();
                }
            }
        }
        return result;
    }

    /**
     * Abstract base for the server-side implementation.
     */
    public abstract static class CalciteRewriterImplBase
        implements io.grpc.BindableService {

        public void rewrite(BytesValue request, StreamObserver<BytesValue> responseObserver) {
            io.grpc.stub.ServerCalls.asyncUnimplementedUnaryCall(REWRITE_METHOD, responseObserver);
        }

        @Override
        public final io.grpc.ServerServiceDefinition bindService() {
            return io.grpc.ServerServiceDefinition.builder(getServiceDescriptor())
                .addMethod(
                    REWRITE_METHOD,
                    asyncUnaryCall(
                        new MethodHandlers<>(this, 0)))
                .build();
        }
    }

    private static final class MethodHandlers<Req, Resp>
        implements io.grpc.stub.ServerCalls.UnaryMethod<Req, Resp>,
                   io.grpc.stub.ServerCalls.ServerStreamingMethod<Req, Resp>,
                   io.grpc.stub.ServerCalls.ClientStreamingMethod<Req, Resp>,
                   io.grpc.stub.ServerCalls.BidiStreamingMethod<Req, Resp> {

        private final CalciteRewriterImplBase serviceImpl;
        private final int methodId;

        MethodHandlers(CalciteRewriterImplBase serviceImpl, int methodId) {
            this.serviceImpl = serviceImpl;
            this.methodId    = methodId;
        }

        @Override
        @SuppressWarnings("unchecked")
        public void invoke(Req request, StreamObserver<Resp> responseObserver) {
            if (methodId == 0) {
                serviceImpl.rewrite((BytesValue) request, (StreamObserver<BytesValue>) responseObserver);
            } else {
                throw new AssertionError("unknown method id: " + methodId);
            }
        }

        @Override public StreamObserver<Req> invoke(StreamObserver<Resp> obs) {
            throw new UnsupportedOperationException();
        }
    }
}
