# resource-claim-image-configurator
Proof-of-concept controller demonstrating DRA Device Binding Conditions (KEP-5007). The controller observes ResourceClaims waiting on binding conditions and mutates the corresponding Pod’s container image once the allocation decision is made, enabling runtime selection of GPU- or CPU-specific images.
