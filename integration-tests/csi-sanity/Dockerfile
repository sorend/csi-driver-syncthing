FROM golang:1.26
RUN go install github.com/kubernetes-csi/csi-test/v5/cmd/csi-sanity@v5.6.0
ENTRYPOINT ["csi-sanity"]
