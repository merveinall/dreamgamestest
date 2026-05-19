# Stage 1: Build — JDK + Maven needed only here
FROM eclipse-temurin:11-jdk-alpine AS build
WORKDIR /app

# Copy dependency manifest first to exploit layer caching
COPY mvnw pom.xml ./
COPY .mvn/ .mvn/
RUN ./mvnw dependency:go-offline -q

# Now copy source and build; cache above is reused unless pom.xml changes
COPY src/ src/
RUN ./mvnw package -DskipTests -q

# Stage 2: Runtime — JRE only, no build tools
FROM eclipse-temurin:11-jre-alpine AS runtime

RUN addgroup -S appgroup && adduser -S appuser -G appgroup

WORKDIR /app
COPY --from=build /app/target/app-0.0.1-SNAPSHOT.jar app.jar

USER appuser
EXPOSE 9001

# Exec form avoids a shell process and prevents signal masking
ENTRYPOINT ["java", "-jar", "app.jar"]
