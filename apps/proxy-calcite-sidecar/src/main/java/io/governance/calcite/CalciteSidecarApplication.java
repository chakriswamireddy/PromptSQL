package io.governance.calcite;

import org.springframework.boot.SpringApplication;
import org.springframework.boot.autoconfigure.SpringBootApplication;
import org.springframework.cache.annotation.EnableCaching;

/**
 * Entry point for the Calcite gRPC sidecar.
 * Starts the gRPC server (port 9095 by default) and the Spring Boot actuator
 * HTTP server (port 9096) for health and Prometheus metrics.
 */
@SpringBootApplication
@EnableCaching
public class CalciteSidecarApplication {

    public static void main(String[] args) {
        SpringApplication.run(CalciteSidecarApplication.class, args);
    }
}
