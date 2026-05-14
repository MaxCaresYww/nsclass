# Manual NamespaceClass Test Plan

This plan validates the NamespaceClass operator manually against a Kubernetes
cluster. Run commands from the repository root.

## Scope

Validate that:

- The controller deploys successfully.
- Valid `NamespaceClass` resources become `Ready`.
- `NamespaceClassBinding/default` is the singleton binding and records applied
  resource inventory in status.
- A mapped namespace receives resources from one or multiple classes.
- Binding membership changes and class template changes add, update, and remove
  managed resources.
- Deleting the singleton binding cleans up managed resources.
- Deleting a referenced class is blocked until the binding no longer references
  it.
- Invalid or unsafe cases surface `Ready=False` status conditions without
  overwriting unmanaged objects.

## 1. Set Up An Isolated Cluster

Use a dedicated Kind cluster for manual testing.

```sh
kind create cluster --name nsclass-manual
kubectl config use-context kind-nsclass-manual
IMG=nsclass:manual make docker-build
kind load docker-image nsclass:manual --name nsclass-manual
make install
make deploy IMG=nsclass:manual
kubectl -n nsclass-system rollout status deploy/nsclass-controller-manager --timeout=120s
kubectl get crd namespaceclasses.akuity.io namespaceclassbindings.akuity.io
kubectl get nsclass
kubectl get nsclsb
```

Expected: the controller deployment is available, both CRDs exist, and
`kubectl get nsclass` and `kubectl get nsclsb` work.

If Kind tries to pull an image by digest and Docker Hub fails, check for a local
node image and create the cluster with that tag:

```sh
docker images --format '{{.Repository}}:{{.Tag}} {{.ID}}' | rg '^kindest/node:'
kind create cluster --name nsclass-manual --image kindest/node:v1.35.0
```

## 2. Happy Path: Ready Class Creates Resources

```sh
kubectl apply -f config/samples/nscls_public_net.yaml -f config/samples/nscls_private_net.yaml
kubectl wait --for=condition=Ready nsclass/public-network --timeout=60s
kubectl wait --for=condition=Ready nsclass/private-network --timeout=60s
kubectl create ns nsclass-manual-a
kubectl apply -f - <<'EOF'
apiVersion: akuity.io/v1alpha1
kind: NamespaceClassBinding
metadata:
  name: default
spec:
  mappings:
  - namespace: nsclass-manual-a
    classNames:
    - public-network
EOF
kubectl wait --for=condition=Ready nsclsb/default --timeout=60s
kubectl -n nsclass-manual-a get cm public-net -o yaml
kubectl get nsclsb/default -o jsonpath='{.status.namespaces[0].namespace}{" "}{.status.namespaces[0].resources[0].kind}{" "}{.status.namespaces[0].resources[0].name}{" "}{.status.namespaces[0].resources[0].className}{"\n"}'
```

Expected: `public-net` exists in `nsclass-manual-a`, has
`data.example=public_net`, and includes:

Labels:
- `namespaceclass.akuity.io/managed: "true"`
- `namespaceclass.akuity.io/class: public-network`

The binding status inventory records `nsclass-manual-a ConfigMap public-net
public-network`.

## 3. Multi-Class Binding Creates Composed Resources

```sh
kubectl create ns nsclass-manual-composed
kubectl patch nsclsb/default --type=json -p='[{"op":"add","path":"/spec/mappings/-","value":{"namespace":"nsclass-manual-composed","classNames":["public-network","private-network"]}}]'
kubectl wait --for=condition=Ready nsclsb/default --timeout=60s
kubectl -n nsclass-manual-composed get cm public-net -o yaml
kubectl -n nsclass-manual-composed get cm private-net -o yaml
kubectl get nsclsb/default -o jsonpath='{range .status.namespaces[*]}{.namespace}{": "}{range .resources[*]}{.name}{"("}{.className}{") "}{end}{"\n"}{end}'
```

Expected: resources from both classes exist in `nsclass-manual-composed`, and
the binding inventory lists the managed resources for both mapped namespaces.

## 4. Membership Change Replaces Managed Resources

```sh
kubectl patch nsclsb/default --type=merge -p '{"spec":{"mappings":[{"namespace":"nsclass-manual-a","classNames":["private-network"]},{"namespace":"nsclass-manual-composed","classNames":["public-network","private-network"]}]}}'
kubectl wait --for=condition=Ready nsclsb/default --timeout=60s
kubectl -n nsclass-manual-a get cm private-net -o yaml
kubectl -n nsclass-manual-a get cm public-net
kubectl get nsclsb/default -o jsonpath='{range .status.namespaces[?(@.namespace=="nsclass-manual-a")].resources[*]}{.name}{" "}{.className}{"\n"}{end}'
```

Expected: `private-net` is created with `data.example=private_net`, `public-net`
returns NotFound, and the inventory for `nsclass-manual-a` only lists
`private-net private-network`.

## 5. Class Update Adds, Updates, And Removes Resources

First add a second resource to the selected class, then replace the template set.

```sh
cat <<'EOF' | kubectl apply -f -
apiVersion: akuity.io/v1alpha1
kind: NamespaceClass
metadata:
  name: private-network
spec:
  resources:
  - apiVersion: v1
    kind: ConfigMap
    metadata:
      name: private-net
    data:
      example: private_net
  - apiVersion: v1
    kind: ConfigMap
    metadata:
      name: old-net
    data:
      example: old
EOF
kubectl wait --for=condition=Ready nsclass/private-network --timeout=60s
for i in {1..60}; do
  kubectl -n nsclass-manual-a get cm old-net >/dev/null 2>&1 && break
  sleep 1
done
kubectl -n nsclass-manual-a get cm old-net -o yaml

cat <<'EOF' | kubectl apply -f -
apiVersion: akuity.io/v1alpha1
kind: NamespaceClass
metadata:
  name: private-network
spec:
  resources:
  - apiVersion: v1
    kind: ConfigMap
    metadata:
      name: private-net
    data:
      example: private_net_v2
  - apiVersion: v1
    kind: ConfigMap
    metadata:
      name: extra-net
    data:
      example: extra
EOF
kubectl wait --for=condition=Ready nsclass/private-network --timeout=60s
kubectl wait --for=jsonpath='{.data.example}'=private_net_v2 -n nsclass-manual-a cm/private-net --timeout=60s
for i in {1..60}; do
  kubectl -n nsclass-manual-a get cm extra-net >/dev/null 2>&1 && break
  sleep 1
done
for i in {1..60}; do
  kubectl -n nsclass-manual-a get cm old-net >/dev/null 2>&1 || break
  sleep 1
done
kubectl -n nsclass-manual-a get cm private-net -o yaml
kubectl -n nsclass-manual-a get cm extra-net -o yaml
kubectl -n nsclass-manual-a get cm old-net
```

Expected: `private-net.data.example` changes to `private_net_v2`, `extra-net`
is created, and `old-net` returns NotFound.

## 6. Deleting A Binding Cleans Up Managed Resources

```sh
kubectl delete nsclsb/default
kubectl wait --for=delete nsclsb/default --timeout=60s
kubectl -n nsclass-manual-a get cm -l namespaceclass.akuity.io/managed=true
kubectl -n nsclass-manual-composed get cm -l namespaceclass.akuity.io/managed=true
```

Expected: no managed ConfigMaps remain in either namespace.

## 7. Referenced NamespaceClass Deletion Is Blocked

```sh
cat <<'EOF' | kubectl apply -f -
apiVersion: akuity.io/v1alpha1
kind: NamespaceClass
metadata:
  name: delete-blocked
spec:
  resources:
  - apiVersion: v1
    kind: ConfigMap
    metadata:
      name: delete-blocked-config
    data:
      state: managed
EOF
kubectl wait --for=condition=Ready nsclass/delete-blocked --timeout=60s
kubectl create ns nsclass-manual-delete-blocked
kubectl apply -f - <<'EOF'
apiVersion: akuity.io/v1alpha1
kind: NamespaceClassBinding
metadata:
  name: default
spec:
  mappings:
  - namespace: nsclass-manual-delete-blocked
    classNames:
    - delete-blocked
EOF
kubectl wait --for=condition=Ready nsclsb/default --timeout=60s
kubectl delete nsclass/delete-blocked --wait=false
kubectl wait --for=condition=Ready=False nsclass/delete-blocked --timeout=60s
kubectl get nsclass/delete-blocked -o jsonpath='{.metadata.deletionTimestamp}{" "}{.status.conditions[?(@.type=="Ready")].reason}{"\n"}'
kubectl delete nsclsb/default
kubectl wait --for=delete nsclass/delete-blocked --timeout=90s
```

Expected: deletion is initially blocked, the class remains with a deletion
timestamp, and the `Ready` condition reason is
`NamespaceClassBindingsStillUseClass`. After deleting the binding, the class is
eventually deleted.

## 8. Negative Case: Missing Namespace

```sh
kubectl apply -f - <<'EOF'
apiVersion: akuity.io/v1alpha1
kind: NamespaceClassBinding
metadata:
  name: default
spec:
  mappings:
  - namespace: nsclass-manual-missing-namespace
    classNames:
    - public-network
EOF
kubectl wait --for=condition=Ready=False nsclsb/default --timeout=60s
kubectl get nsclsb/default -o jsonpath='{.status.conditions[?(@.type=="Ready")].reason}{"\n"}'
kubectl create ns nsclass-manual-missing-namespace
kubectl wait --for=condition=Ready nsclsb/default --timeout=60s
```

Expected: the binding first reports `MissingNamespace`. After the namespace is
created, the namespace watch reconciles the binding and resources are applied.

## 9. Negative Case: Missing Class

```sh
kubectl create ns nsclass-manual-missing-class
kubectl apply -f - <<'EOF'
apiVersion: akuity.io/v1alpha1
kind: NamespaceClassBinding
metadata:
  name: default
spec:
  mappings:
  - namespace: nsclass-manual-missing-class
    classNames:
    - does-not-exist
EOF
kubectl wait --for=condition=Ready=False nsclsb/default --timeout=60s
kubectl get nsclsb/default -o jsonpath='{.status.conditions[?(@.type=="Ready")].reason}{"\n"}'
kubectl get nsclsb/default -o jsonpath='{.status.namespaces}{"\n"}'
```

Expected: the binding `Ready` condition is `False` with reason
`MissingNamespaceClass`, and no managed resource inventory is recorded for the
missing class.

## 10. Negative Case: Invalid Template

```sh
cat <<'EOF' | kubectl apply -f -
apiVersion: akuity.io/v1alpha1
kind: NamespaceClass
metadata:
  name: invalid-template
spec:
  resources:
  - apiVersion: v1
    kind: ConfigMap
    metadata: {}
EOF
kubectl wait --for=condition=Ready=False nsclass/invalid-template --timeout=60s
kubectl get nsclass invalid-template -o yaml
```

Expected: `status.observedGeneration` matches `metadata.generation`; the
`Ready` condition is `False` with reason `TemplateInvalid` and a message
mentioning `metadata.name`.

## 11. Negative Case: Cluster-Scoped Template

```sh
cat <<'EOF' | kubectl apply -f -
apiVersion: akuity.io/v1alpha1
kind: NamespaceClass
metadata:
  name: cluster-scoped-template
spec:
  resources:
  - apiVersion: v1
    kind: Namespace
    metadata:
      name: should-not-be-created
EOF
kubectl wait --for=condition=Ready=False nsclass/cluster-scoped-template --timeout=60s
kubectl get nsclass cluster-scoped-template -o yaml
kubectl get ns should-not-be-created
```

Expected: the `Ready` condition is `False` with reason
`UnsupportedClusterScopedResource`; `should-not-be-created` does not exist.

## 12. Negative Case: Duplicate Desired Object

```sh
cat <<'EOF' | kubectl apply -f -
apiVersion: akuity.io/v1alpha1
kind: NamespaceClass
metadata:
  name: duplicate-template
spec:
  resources:
  - apiVersion: v1
    kind: ConfigMap
    metadata:
      name: duplicate-config
    data:
      first: one
  - apiVersion: v1
    kind: ConfigMap
    metadata:
      name: duplicate-config
    data:
      second: two
EOF
kubectl wait --for=condition=Ready nsclass/duplicate-template --timeout=60s
kubectl create ns nsclass-manual-dup
kubectl apply -f - <<'EOF'
apiVersion: akuity.io/v1alpha1
kind: NamespaceClassBinding
metadata:
  name: default
spec:
  mappings:
  - namespace: nsclass-manual-dup
    classNames:
    - duplicate-template
EOF
kubectl wait --for=condition=Ready=False nsclsb/default --timeout=60s
kubectl get nsclsb/default -o jsonpath='{.status.conditions[?(@.type=="Ready")].reason}{"\n"}'
kubectl -n nsclass-manual-dup get cm duplicate-config
```

Expected: the binding `Ready` condition is `False` with reason
`DuplicateDesiredObject`, and `duplicate-config` is not created.

## 13. Negative Case: Existing Unmanaged Object Is Not Adopted

```sh
cat <<'EOF' | kubectl apply -f -
apiVersion: akuity.io/v1alpha1
kind: NamespaceClass
metadata:
  name: conflict-template
spec:
  resources:
  - apiVersion: v1
    kind: ConfigMap
    metadata:
      name: conflict-config
    data:
      class: managed
EOF
kubectl wait --for=condition=Ready nsclass/conflict-template --timeout=60s
kubectl create ns nsclass-manual-conflict
kubectl -n nsclass-manual-conflict create cm conflict-config --from-literal=user=owned
kubectl apply -f - <<'EOF'
apiVersion: akuity.io/v1alpha1
kind: NamespaceClassBinding
metadata:
  name: default
spec:
  mappings:
  - namespace: nsclass-manual-conflict
    classNames:
    - conflict-template
EOF
kubectl wait --for=condition=Ready=False nsclsb/default --timeout=60s
kubectl get nsclsb/default -o jsonpath='{.status.conditions[?(@.type=="Ready")].reason}{"\n"}'
kubectl -n nsclass-manual-conflict get cm conflict-config -o yaml
```

Expected: the binding `Ready` condition is `False` with reason
`ManagedResourceConflict`. The existing ConfigMap still has `data.user=owned`,
does not gain the class template data, and does not gain
`namespaceclass.akuity.io/managed=true`.

## 14. Negative Case: Non-Default Binding Name Is Rejected

```sh
kubectl apply -f - <<'EOF'
apiVersion: akuity.io/v1alpha1
kind: NamespaceClassBinding
metadata:
  name: not-default
spec:
  mappings:
  - namespace: default
    classNames:
    - public-network
EOF
```

Expected: the API server rejects the resource because
`NamespaceClassBinding` must be named `default`.

## 15. Cleanup

```sh
kubectl delete nsclsb/default --ignore-not-found
kubectl wait --for=delete nsclsb/default --timeout=60s || true
kubectl delete ns nsclass-manual-a nsclass-manual-composed nsclass-manual-delete-blocked nsclass-manual-missing-namespace nsclass-manual-missing-class nsclass-manual-dup nsclass-manual-conflict --ignore-not-found
kubectl delete nsclass public-network private-network invalid-template cluster-scoped-template duplicate-template conflict-template delete-blocked --ignore-not-found
make undeploy ignore-not-found=true
make uninstall ignore-not-found=true
kind delete cluster --name nsclass-manual
```

## Pass Criteria

The manual test passes when the controller deploys successfully, valid classes
become Ready, selected namespaces receive only expected managed resources,
status inventories match the applied resources, membership and class changes
converge, binding deletion cleans up managed resources, referenced class
deletion is blocked, and all negative cases produce expected status conditions
without overwriting unmanaged objects.
