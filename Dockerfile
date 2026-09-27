# Build a static TDMS binary, then ship it on a minimal, non-root base image.
FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/tdms ./cmd/tdms

# distroless/static carries the CA certificates TDMS needs for HTTPS calls to
# QMetry, PSS and Supabase; timezone data is compiled into the binary.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/tdms /tdms
EXPOSE 8080
ENTRYPOINT ["/tdms"]
CMD ["serve", "--addr", ":8080"]
