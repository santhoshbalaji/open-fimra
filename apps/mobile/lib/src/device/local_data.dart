/// Everything the field app keeps on the phone, and how a wipe deletes it.
///
/// Add a step here when the app stores a new kind of field data. The steps are built in
/// `AppServices.connect`; see the field app guide for the list in words.
class LocalData {
  LocalData(this.steps);

  final List<WipeStep> steps;

  /// Runs every step, even when one fails, and returns the names of any
  /// that failed.
  Future<List<String>> wipe() async {
    final failed = <String>[];
    for (final step in steps) {
      try {
        await step.run();
      } on Object {
        failed.add(step.name);
      }
    }
    return failed;
  }
}

/// One kind of data on the phone and how to delete it.
class WipeStep {
  const WipeStep(this.name, this.run);

  /// What is deleted, in plain English.
  final String name;
  final Future<void> Function() run;
}

/// The field data on this phone, in the order a wipe deletes it. Each
/// argument deletes one kind of data.
LocalData fieldDataOnPhone({
  required Future<void> Function() notifications,
  required Future<void> Function() pushRegistration,
  required Future<void> Function() offlineMaps,
  Future<void> Function()? offlineReports,
  Future<void> Function()? mediaCache,
  required Future<void> Function() deviceId,
  required Future<void> Function() signInTokens,
}) => LocalData([
  WipeStep('Alert notifications shown on the phone', notifications),
  WipeStep('The push registration with the push app', pushRegistration),
  WipeStep('Map areas saved for offline use', offlineMaps),
  if (offlineReports != null) WipeStep('Saved alerts, maps, forms and queued reports', offlineReports),
  if (mediaCache != null) WipeStep('Temporary photo copies', mediaCache),
  WipeStep("This phone's registration ID", deviceId),
  WipeStep('Sign-in tokens', signInTokens),
]);
