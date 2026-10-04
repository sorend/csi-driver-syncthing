# Cross-node PVC example

This example creates a PVC using the `syncthing` StorageClass, writes a log on one node, then tails the replicated log from a pod scheduled on a different node.

The driver supports `ReadWriteOnce` and one active writer node per volume. Run the pods in sequence: after the writer finishes and releases the claim, the reader attaches it on another node and waits for the initial replica sync. The reader's required pod anti-affinity keeps it off the writer's node. Keep the completed writer pod until the reader is scheduled; the cluster needs at least two schedulable nodes.

Apply the PVC and writer:

```sh
kubectl apply -f example/pvc.yaml
kubectl apply -f example/writer-pod.yaml
kubectl wait --for=jsonpath='{.status.phase}'=Succeeded pod/syncthing-example-writer --timeout=5m
```

Start the reader on the other node and follow its output:

```sh
kubectl apply -f example/reader-pod.yaml
kubectl logs -f syncthing-example-reader
```

Check that the pods landed on different nodes with `kubectl get pods -o wide`. The reader follows `/data/activity.log` with `tail -F`; it prints the lines written by the first pod and remains attached to follow later changes to the file.

Delete the reader and writer pods before deleting the claim:

```sh
kubectl delete -f example/reader-pod.yaml
kubectl delete -f example/writer-pod.yaml
kubectl delete -f example/pvc.yaml
```
