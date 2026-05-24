# syntax=docker/dockerfile:1.7
FROM eclipse-temurin:21-jdk-bookworm AS builder
WORKDIR /workspace
COPY pom.xml .
# Download deps first (layer cache)
RUN --mount=type=cache,target=/root/.m2 \
    mvn dependency:go-offline -q
COPY src/ src/
RUN --mount=type=cache,target=/root/.m2 \
    mvn package -DskipTests -q

FROM eclipse-temurin:21-jre-bookworm
WORKDIR /app
# Create non-root user
RUN groupadd -r calcite && useradd -r -g calcite calcite
COPY --from=builder /workspace/target/proxy-calcite-sidecar-*.jar app.jar
USER calcite
EXPOSE 9095 9096
ENTRYPOINT ["java", \
  "-Xmx512m", \
  "-Xms128m", \
  "-XX:+UseZGC", \
  "-XX:+OptimizeStringConcat", \
  "-jar", "app.jar"]
