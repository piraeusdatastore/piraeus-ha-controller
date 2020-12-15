# Piraeus High Availability Controller

[![GitHub release (latest by date)](https://img.shields.io/github/v/release/piraeusdatastore/piraeus-ha-controller)](https://github.com/piraeusdatastore/piraeus-ha-controller/releases)
![tests](https://github.com/piraeusdatastore/piraeus-ha-controller/workflows/tests/badge.svg)

The Piraeus High Availability Controller will speed up the fail over process for stateful workloads using [Piraeus] for
storage.

[Piraeus]: https://piraeus.io

## Get started

The Piraeus High Availability Controller can be deployed as part of the [Piraeus Operator].

If you want to get started directly with an existing Piraeus setup, check out the [single file deployment](./deploy/all.yaml)
The deployment will create:

* A namespace `ha-controller`
* All needed RBAC resources
* A Deployment spawning 3 replicas of the Piraeus High Availability Controller, configured to connect to `http://piraeus-op-cs.default.svc`

Copy the file, make any desired changes (see the [options](#options) below) and apply:

```
$ kubectl apply -f deploy/all.yaml
namespace/ha-controller created
deployment.apps/piraeus-ha-controller created
serviceaccount/ha-controller created
clusterrole.rbac.authorization.k8s.io/ha-controller created
role.rbac.authorization.k8s.io/ha-controller created
clusterrolebinding.rbac.authorization.k8s.io/ha-controller created
rolebinding.rbac.authorization.k8s.io/ha-controller created
$ kubectl -n ha-controller get pods
NAME                                    READY   STATUS         RESTARTS   AGE
piraeus-ha-controller-b7c848b89-bwb78   1/1     Running        0          20s
piraeus-ha-controller-b7c848b89-ljwcn   1/1     Running        0          20s
piraeus-ha-controller-b7c848b89-ml84m   1/1     Running        0          20s
```

### Deploy your stateful workloads

To mark your stateful applications as managed by Piraeus, use the `linstor.csi.linbit.com/on-storage-lost: remove` label.
For example, Pod Templates in a StatefulSet should look like:

```yaml
apiVersion: apps/v1
kind: StatefulSet
metadata:
  name: my-stateful-app
spec:
  serviceName: my-stateful-app
  selector:
    matchLabels:
      app.kubernetes.io/name: my-stateful-app
  template:
    metadata:
      labels:
        app.kubernetes.io/name: my-stateful-app
        linstor.csi.linbit.com/on-storage-lost: remove
    ...
```

This way, the Piraeus High Availability Controller will not interfere with applications that do not benefit or even
support it's primary use.

### Options

To configure the connection to your Piraeus/LINSTOR controller, use the environment variables described [here](https://pkg.go.dev/github.com/LINBIT/golinstor/client#NewClient)

The Piraeus High Availability Controller itself can be configured using the following flags:

```
--attacher-name string                   name of the attacher to consider (default "linstor.csi.linbit.com")
--known-resource-grace-period duration   grace period for known resources after which promotable resources will be considered lost (default 45s)
--kubeconfig string                      path to kubeconfig file
--leader-election                        use kubernetes leader election
--leader-election-healtz-port int        port to use for serving the /healthz endpoint (default 8080)
--leader-election-lease-name string      name for leader election lease (unique for each pod)
--leader-election-namespace string       namespace for leader election
--new-resource-grace-period duration     grace period for newly created resources after which promotable resources will be considered lost (default 45s)
--pod-label-selector string              labels selector for pods to consider (default "linstor.csi.linbit.com/on-storage-lost=remove")
--reconcile-interval duration            time between reconciliation runs (default 10s)
--v int32                                set log level (default 4)
```

[Piraeus Operator]: https://github.com/piraeusdatastore/piraeus-operator

## What & Why?

Let's say you are using Piraeus to provision your Kubernetes PersistentVolumes. You replicate your volumes across
multiple nodes in your cluster, so that even if a node crashes, a simple re-creation of the Pod will still have access
to the same data.

### The Problem

We have deployed our application as a StatefulSet to ensure only one Pod can access the PersistentVolume at a time,
even in case of node failures.

```
$ kubectl get pod -o wide
NAME                                        READY   STATUS              RESTARTS   AGE     IP                NODE                    NOMINATED NODE   READINESS GATES
my-stateful-app-with-piraeus                1/1     Running             0          5m      172.31.0.1        node01.ha.cluster       <none>           <none>
```

Now we simulate our node crashing and wait for Kubernetes to recognize the node as unavailable

```
$ kubectl get nodes
NAME                    STATUS     ROLES     AGE    VERSION
master01.ha.cluster     Ready      master    12d    v1.19.4
master02.ha.cluster     Ready      master    12d    v1.19.4
master03.ha.cluster     Ready      master    12d    v1.19.4
node01.ha.cluster       Ready      compute   12d    v1.19.4
node02.ha.cluster       Ready      compute   12d    v1.19.4
node03.ha.cluster       NotReady   compute   12d    v1.19.4
```

We check our pod again:

```
$ kubectl get pod -o wide
NAME                                        READY   STATUS              RESTARTS   AGE     IP                NODE                    NOMINATED NODE   READINESS GATES
my-stateful-app-with-piraeus-0              1/1     Running             0          10m     172.31.0.1        node01.ha.cluster       <none>           <none>
```

Nothing happened! That's because Kubernetes, by default, adds a 5-minute grace period before pods are evicted from
unreachable nodes. So we wait.

```
$ kubectl get pod -o wide
NAME                                        READY   STATUS              RESTARTS   AGE     IP                NODE                    NOMINATED NODE   READINESS GATES
my-stateful-app-with-piraeus-0              1/1     Terminating         0          15m     172.31.0.1        node01.ha.cluster       <none>           <none>
```

Now our Pod is `Terminating`, but still nothing happens. You force delete the pod

```
$ kubectl delete pod my-stateful-app-with-piraeus-0 --force
warning: Immediate deletion does not wait for confirmation that the running resource has been terminated. The resource may continue to run on the cluster indefinitely.
pod "my-stateful-app-with-piraeus-0" force deleted
$ kubectl get pod -o wide
NAME                                        READY   STATUS              RESTARTS   AGE     IP                NODE                    NOMINATED NODE   READINESS GATES
my-stateful-app-with-piraeus-0              0/1     ContainerCreating   0          5s      172.31.0.1        node02.ha.cluster       <none>           <none>
```

Still, nothing happens, the new Pod is assigned to a different node, but it cannot start. Why? Because Kubernetes thinks the old volume might still be attached

```
$ kubectl describe pod my-stateful-app-with-piraeus-0
...
Events:                                                                                                                                                                                       
  Type     Reason                  Age               From                            Message                                                                                                  
  ----     ------                  ----              ----                            -------                                                                                                  
  Normal   Scheduled               <unknown>         default-scheduler               Successfully assigned default/my-stateful-app-with-piraeus-0 to node02.ha.cluster
  Warning  FailedAttachVolume      28s               attachdetach-controller         Multi-Attach error for volume "pvc-9d991a74-0713-448f-ac0c-0b20b842763e" Volume is already exclusively at
tached to one node and can't be attached to another
```

This eventually times out, and we eventually our Pod will be running on another node.

```
$ kubectl get pod -o wide
NAME                                        READY   STATUS              RESTARTS   AGE     IP                NODE                    NOMINATED NODE   READINESS GATES
my-stateful-app-with-piraeus-0              1/1     Running             0          5m      172.31.0.1        node02.ha.cluster       <none>           <none>
```

This process can take up to 15 minutes using the default settings of Kubernetes.

### The solution

The Piraeus High Availability Controller can speed up this fail-over process significantly. As before, we start out with a running pod:

```
$ kubectl get pod -o wide
NAME                                        READY   STATUS              RESTARTS   AGE     IP                NODE                    NOMINATED NODE   READINESS GATES
my-stateful-app-with-piraeus                1/1     Running             0          10s     172.31.0.1        node01.ha.cluster       <none>           <none>
```

Again, we simulate our node crashing and wait for Kubernetes to recognize the node as unavailable

```
$ kubectl get nodes
NAME                    STATUS     ROLES     AGE    VERSION
master01.ha.cluster     Ready      master    12d    v1.19.4
master02.ha.cluster     Ready      master    12d    v1.19.4
master03.ha.cluster     Ready      master    12d    v1.19.4
node01.ha.cluster       Ready      compute   12d    v1.19.4
node02.ha.cluster       Ready      compute   12d    v1.19.4
node03.ha.cluster       NotReady   compute   12d    v1.19.4
```

We check our pod again. After a short wait (by default after around 45seconds after the node "crashed"):

```
$ kubectl get pod -o wide
NAME                                        READY   STATUS              RESTARTS   AGE     IP                NODE                    NOMINATED NODE   READINESS GATES
my-stateful-app-with-piraeus-0              0/1     ContainerCreating   0          3s      172.31.0.1        node02.ha.cluster       <none>           <none>
```

We see that the pod was rescheduled to another node. We can also take a look the cluster events:

```
$ kubectl get events --sort-by=.metadata.creationTimestamp -w
...
0s          Warning   ForceDeleted              pod/my-stateful-app-with-piraeus-0                                                      pod deleted because a used volume is marked as failing
0s          Warning   ForceDetached             volumeattachment/csi-d2b994ff19d526ace7059a2d8dea45146552ed078d00ed843ac8a8433c1b5f6f   volume detached because it is marked as failing
...
```

### How?

The Piraeus High Availability Controller connects to your Piraeus Controller, which in turn is connected to the 
Satellites. It attaches to the event log generated by the controller, which contains information about the promotion 
statuses of all volume replicas.

To "promote" a volume means to make it the primary replica, the only replica in the cluster allowed to write to the
volume. In case non-primary replicas suddenly report that they could be promoted, we can deduce that the current primary
is no longer considered active. This means that even if the active primary continues running and allowing writes to the
volume, writes would not be propagated through the cluster.

As a consequence, we can safely re-schedule Pods using the disconnected replica. To do this quickly, we have to:

* Delete the Pod using the old replica. This can be done without waiting for confirmation from the node. As discussed
  above, writes initiated by the Pod will no longer propagate through the cluster
  
* Delete the volume attachment for the node. This frees up Kubernetes to attach the volume to another node. This 
  prevents `Multi-attach` errors for Read-Write-Once volumes.
  
## Development

You can run the program on your own machine with debugger, as long as you can access a Kubernetes cluster and the LINSTOR API.
To get access to the LINSTOR API from outside the Kubernetes cluster, you can use a `NodePort` service:

```yaml
apiVersion: v1
kind: Service
metadata:
  name: linstor-ext
spec:
  type: NodePort
  selector:
    app: piraeus-op-cs
    role: piraeus-controller
  ports:
  - port: 3370
    nodePort: 30370
```

Use the `--kubeconfig` option to configure access to the Kubernetes API.

There are some basic unit tests you can run with `go test ./...`
