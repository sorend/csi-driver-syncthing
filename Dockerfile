FROM golang:1.23 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /out/manager ./cmd/manager \
    && CGO_ENABLED=0 go build -o /out/csi-node ./cmd/csi-node \
    && CGO_ENABLED=0 go build -o /out/node-agent ./cmd/node-agent

FROM gcr.io/distroless/static:nonroot AS manager
COPY --from=build /out/manager /manager
COPY --from=build /out/csi-node /csi-node
COPY --from=build /out/node-agent /node-agent
USER 65532:65532
ENTRYPOINT ["/manager"]

FROM gcr.io/distroless/static:nonroot AS csi-node
COPY --from=build /out/csi-node /csi-node
USER 65532:65532
ENTRYPOINT ["/csi-node"]

FROM gcr.io/distroless/static:nonroot AS node-agent
COPY --from=build /out/node-agent /node-agent
USER 65532:65532
ENTRYPOINT ["/node-agent"]
