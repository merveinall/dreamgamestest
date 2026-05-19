# Dockerfile Design Decisions

## Application summary

| Property   | Value                        |
|------------|------------------------------|
| Framework  | Spring Boot 2.6.1            |
| Java       | 11                           |
| Build tool | Maven (with wrapper)         |
| JAR        | `app-0.0.1-SNAPSHOT.jar`     |
| Port       | 9001                         |
| Image ref  | `merveinal1/sample-java-app:v1.0` |

---

## Build acceleration

**Dependency caching layer**

```dockerfile
COPY mvnw pom.xml ./
COPY .mvn/ .mvn/
RUN ./mvnw dependency:go-offline -q

COPY src/ src/
RUN ./mvnw package -DskipTests -q
```

The `pom.xml` and Maven wrapper are copied before `src/`. Docker rebuilds layers in order, so the `dependency:go-offline` step is only re-executed when `pom.xml` changes. Routine code changes only re-execute the `package` step, which is fast because all deps are already in the local cache inside the build layer.

**`-q` (quiet) flag** suppresses Maven download noise, keeping build output readable in CI logs.

**`-DskipTests`** is safe here because the image build is not the test stage; tests run separately in the CI pipeline (Jenkins) with full source context.

---

## Image size reduction

| Choice | Alternative | Why |
|--------|-------------|-----|
| `eclipse-temurin:11-jre-alpine` runtime | `openjdk:11` | Alpine base is ~5 MB vs ~70 MB for debian slim; JRE excludes compiler, javac, and tools (saves ~180 MB vs JDK) |
| Multi-stage build | Single stage | The final image contains only the JRE + JAR; Maven, `.m2/` cache, and JDK binaries are discarded |
| Copy single JAR, rename to `app.jar` | Copy entire `target/` | Excludes test classes, sources jar, and Maven metadata from the image |
| `.dockerignore` excludes `target/`, `docs/`, `kubernetes/` etc. | No ignore file | Reduces build context sent to the Docker daemon — avoids slow uploads and prevents accidental inclusion of secrets |

---

## Security decisions

**Non-root user**

```dockerfile
RUN addgroup -S appgroup && adduser -S appuser -G appgroup
USER appuser
```

Running as root inside a container means a container escape grants root on the host. The system (`-S`) user has no shell, no home directory write access, and no password — minimal attack surface.

**Exec-form ENTRYPOINT**

```dockerfile
ENTRYPOINT ["java", "-jar", "app.jar"]
```

Shell form (`CMD java -jar ...`) spawns a `/bin/sh -c` wrapper. The exec form runs the JVM directly as PID 1, which:
- Receives OS signals (SIGTERM) correctly — enables graceful shutdown
- Avoids a shell process that could be exploited for command injection

**`readOnlyRootFilesystem: true`** is set in the Kubernetes `securityContext`. Spring Boot needs `/tmp` for Tomcat work files, so a `tmp` emptyDir volume is mounted at `/tmp` in [kubernetes/apps/deployment.yaml](../kubernetes/apps/deployment.yaml).

**Minimal Alpine base** has fewer packages and a smaller CVE surface than Debian or Ubuntu. Regular base image updates (`eclipse-temurin:11-jre-alpine` is maintained by the Adoptium project) keep known CVEs patched.

**No `COPY . .`** — only the files the runtime actually needs are in the final stage. Secrets, `.env` files, or local config are never accidentally included.

**`allowPrivilegeEscalation: false` and `capabilities: drop: ALL`** are set in the Deployment securityContext as additional hardening at the Kubernetes layer.
