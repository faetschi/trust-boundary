# Setup routes

Choose the guide that matches the task:

- **Run a local Go fixture:** [launch](launch.md). The fixture is non-claim-bearing
  and does not start Pi.
- **Resume the existing Windows Hyper-V Ubuntu guest:** [launch](launch.md#existing-windows-hyper-v-vm).
- **Create or provision a new Linux host or Hyper-V guest:** [reproducible setup](reproducible-setup.md), then follow the linked
  [host/VM](../infra/hyperv/SETUP.md) and [guest](../infra/guest/README.md)
  runbooks.
- **Run offline verification in the dedicated guest:** use the guest
  [offline runbook](../infra/guest/README.md). This verifies code and host
  prerequisites; it does not establish containment or G1.
- **Install Pi and TBound:** [install](install.md). Install Pi inside Linux,
  including when the host machine runs Windows.

The scripted clean-host setup has not been qualified end to end. The existing
reference VM is development infrastructure and is not a clean G1 baseline.
Native Windows is not a governed execution profile. Current gates are in
[status](status.md).
