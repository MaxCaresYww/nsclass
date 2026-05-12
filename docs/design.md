# Design Notes

This document collects draft architecture decisions for NamespaceClass.

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

Membership is represented as a comma-separated annotation:

```yaml
namespaceclass.akuity.io/names: public-network,registry-push
```

Kubernetes labels are designed for simple identifying metadata and selector
queries, not structured lists. Using an annotation keeps the multi-class value
as controller configuration.

The controller treats the selected classes as the source of all managed
templates for the namespace. If membership changes, resources from removed
classes should be removed and resources from newly added classes should be
applied.

