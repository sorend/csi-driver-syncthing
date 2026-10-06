FROM golang:1.26
RUN go install github.com/kubernetes-csi/csi-test/v5/cmd/csi-sanity@v5.6.0
COPY create-mount-path.sh remove-mount-path.sh create-staging-path.sh remove-staging-path.sh /usr/local/bin/
RUN chmod 0755 /usr/local/bin/create-mount-path.sh /usr/local/bin/remove-mount-path.sh /usr/local/bin/create-staging-path.sh /usr/local/bin/remove-staging-path.sh
COPY volume-parameters.yaml /etc/csi-sanity/volume-parameters.yaml
ENTRYPOINT ["csi-sanity"]
