# Design Notes

This document collects draft architecture decisions for NamespaceClass.

## Controller Responsibilities

The NamespaceClass controller owns the lifecycle of NamespaceClass definitions.
It validates resource templates, reports class readiness, and blocks deletion while
a class is still referenced by the default NamespaceClassBinding. It does not
apply class resources into namespaces.

It watches NamespaceClass custom resource events:
- Create: validate resources in spec.
- Update: validate resources in spec.
- Delete: Check if there is NamespaceClassBinding still refers to this NamespaceClass
and block deletion if yes.

The NamespaceClassBinding controller owns class assignment and resource
reconciliation. It reads `NamespaceClassBinding/default`, resolves each
namespace-to-class mapping, applies the selected class resources into target
namespaces, records the applied resource inventory, and removes resources that
are no longer desired.

It watches following events:

**1. Create, Update, Delete event from NamespaceClassBinding.**

**2. Create, Update, Delete (not needed) event from NamespaceClass.**

Support sync resource changes from NamespaceClass to the system.

**3. Create event from Namespace**

Support specify mapping from namespace to namespaceClass in namespaceClassBinding
when namespace is not yet exist. Then after namespace is created, the resources
specified in associated namespaceClass will get created automatically.

## Architecture Decisions

### Which `Group` Should Be Used for the NamespaceClass CRD?

If there is a clear split of CRDs by functional area, it is reasonable to
define multiple groups. Currently, that split is not clear, so using `acuity.io`
is the safer choice. That is why `--group` is omitted in `kubebuilder create api`
command.

### Namespaced or Cluster for NamespaceClass CRD scope?

Use cluster-scoped resources.

In a product cluster with privilege management, NamespaceClass resources are
intended for cluster administrators. Tenant users might need read access to
these resources, but they should not be granted edit access.

Namespace management is typically performed by cluster administrators, who
already have cluster-level privileges.

If tenant users want to know the list of available namespace classes and their
associated resources, they can inspect the NamespaceClass resources. With being
cluster-scoped, they do not need to know which namespace contains the resources,
since and they might not have permission to list resources in that namespace.


### How to handle NamespaceClass deletion?

Recorded here: https://github.com/MaxCaresYww/nsclass/issues/4

### Protect NamespaceClass managed reource from manual manipulation

Recorded here: https://github.com/MaxCaresYww/nsclass/issues/3

- How about change in additional to deletion.

### Should One Namespace Belong to One NamespaceClass or Multiple?

Allow one namespace to compose multiple NamespaceClasses.

Membership is represented by the cluster-scoped singleton
`NamespaceClassBinding/default`:

```yaml
apiVersion: akuity.io/v1alpha1
kind: NamespaceClassBinding
metadata:
  name: default
spec:
  mappings:
    - namespace: web-portal
      classNames:
        - public-network
        - registry-push
```

Using a binding keeps class membership out of Namespace labels/annotations and
gives platform admins one assignment table to maintain.

The binding controller treats each mapping's selected classes as the source of
all managed templates for the mapped namespace. If membership changes, resources
from removed classes should be removed and resources from newly added classes
should be applied. If a namespace mapping is removed, managed resources for that
namespace should be removed.

Refer to https://github.com/MaxCaresYww/nsclass/issues/2 for detail.

