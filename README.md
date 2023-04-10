# keda-topology-scaler

## What is this?

Sometimes you want to run specific services (or pods) once every node, physical host, availability zone, region, ....
Kubernetes itself has the possibility to run a pod on every node (or a subset of nodes, through a selector) by utilizing DaemonSets.
Unfortunately, there's no support for running a DaemonSet once per region or Availability Zone. 

One option would be to create an Operator that's ensuring the pods running in the correct area. This project seeks to bridge the gap between HPA and DaemonSets by exposing the number of values for a specific Node Label (a so called `topologyKey`, as per kubernetes terms) towards [keda](https://keda.sh). This allows you to simply define a autoscaling policy "Run one pod per Availability Zone".

## Building

You need a working go + gRPC toolchain, or alternatively build the project directly in a container through the provided Dockerfile.

## Deployment

This service needs access to the Kubernetes APIServer through its serviceAccount and implements the KEDA external-scaler API. That way you can simply run it as a Deployment in your cluster, having a service target it and use it afterwards in KEDA through its service DNS name.

Required RBAC Permissions:

- Nodes
  - get
  - list
  - watch


## Contributing

If you find a bug, or want to request a feature, please submit an issue and/or fork the project and send a PR.
