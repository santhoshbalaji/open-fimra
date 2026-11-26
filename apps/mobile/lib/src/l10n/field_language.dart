import 'dart:ui' show PlatformDispatcher;

import 'package:flutter/material.dart';
import 'package:shared_preferences/shared_preferences.dart';

import '../api/models.dart';
import 'app_localizations.dart';

const _preferenceKey = 'field.locale';

Future<Locale?> savedFieldLocale() async {
  final preferences = await SharedPreferences.getInstance();
  final saved = preferences.getString(_preferenceKey);
  return AppLocalizations.supportedLocales.where((locale) => locale.toLanguageTag() == saved).firstOrNull;
}

Future<void> saveFieldLocale(Locale? locale) async {
  final preferences = await SharedPreferences.getInstance();
  if (locale == null) {
    await preferences.remove(_preferenceKey);
  } else {
    await preferences.setString(_preferenceKey, locale.toLanguageTag());
  }
}

/// Notifications can arrive while the app has no screen or BuildContext.
Future<AppLocalizations> notificationLanguage() async {
  final locale =
      await savedFieldLocale() ??
      basicLocaleListResolution(PlatformDispatcher.instance.locales, AppLocalizations.supportedLocales);
  return lookupAppLocalizations(locale);
}

AppLocalizations fieldText(BuildContext context) => AppLocalizations.of(context);

String fieldLanguageName(String code) {
  for (final locale in AppLocalizations.supportedLocales) {
    if (locale.toLanguageTag() == code || locale.languageCode == code.split('-').first) {
      return lookupAppLocalizations(locale).languageSelfName;
    }
  }
  return switch (code.split('-').first) {
    'ur' => 'اردو',
    'bn' => 'বাংলা',
    _ => code,
  };
}

Future<void> chooseFieldLanguage(BuildContext context, Future<void> Function(Locale?) onChanged) async {
  final choice = await showDialog<String>(
    context: context,
    builder: (context) => SimpleDialog(
      title: Text(fieldText(context).language),
      children: [
        SimpleDialogOption(onPressed: () => Navigator.pop(context, ''), child: Text(fieldText(context).phoneLanguage)),
        for (final locale in AppLocalizations.supportedLocales)
          SimpleDialogOption(
            onPressed: () => Navigator.pop(context, locale.toLanguageTag()),
            child: Text(fieldLanguageName(locale.toLanguageTag())),
          ),
      ],
    ),
  );
  if (choice == null) return;
  await onChanged(
    choice.isEmpty ? null : AppLocalizations.supportedLocales.firstWhere((locale) => locale.toLanguageTag() == choice),
  );
}

String forestName(BuildContext context, ForestClass? forest, [String fallback = '']) {
  final text = fieldText(context);
  return switch (forest) {
    ForestClass.inside => text.insideForest,
    ForestClass.nearEdge => text.nearTheForestEdge,
    ForestClass.outside => text.outsideForest,
    ForestClass.fixedHeatSource => text.fixedHeatSource,
    null => fallback,
  };
}

String queueName(BuildContext context, AlertQueue? queue, [String fallback = '']) {
  final text = fieldText(context);
  return switch (queue) {
    AlertQueue.unit => text.myArea,
    AlertQueue.edgeReview => text.nearAForestBoundary,
    AlertQueue.triage => text.needsAUnit,
    null => fallback,
  };
}

String workflowName(BuildContext context, String label) {
  final text = fieldText(context);
  return switch (label) {
    'New' => text.newAlert,
    'Acknowledged' => text.acknowledged,
    'Team sent' => text.teamSent,
    'Reached site' => text.reachedSite,
    'Controlled' => text.controlled,
    'Extinguished' => text.extinguished,
    'First report' => text.firstReport,
    'Final report' => text.finalReport,
    'Verified' => text.verified,
    'Closed' => text.closed,
    'False alarm' => text.falseAlarm,
    'Duplicate' => text.duplicate,
    'Controlled burn' => text.controlledBurn,
    'Not reachable' => text.notReachable,
    'Report pending' || 'Waiting for a report' => text.waitingForAReport,
    'Awaiting review' => text.awaitingReview,
    _ => label,
  };
}

String phoneName(BuildContext context, Device device) {
  final text = fieldText(context);
  return switch (device.platform) {
    'android' => text.androidPhone,
    'ios' => text.iphone,
    'web' => text.browserTestBuild,
    _ => text.phone,
  };
}

String phoneStatus(BuildContext context, DeviceStatus status) {
  final text = fieldText(context);
  return switch (status.slug) {
    'active' => text.signedIn,
    'signed_out' => text.signedOut,
    'removed' => text.removed,
    'wipe_pending' => text.waitingToDeleteItsData,
    'wiped' => text.dataDeleted,
    _ => status.label,
  };
}

/// Messages from the API and the offline store can have older English text.
String fieldProblem(BuildContext context, String message) {
  final text = fieldText(context);
  return switch (message) {
    'No connection. Saved work stays on this phone.' => text.noConnectionSavedWork,
    'Could not save updates on this phone.' => text.couldNotSaveUpdates,
    'Waiting for a connection.' => text.waitingForConnection,
    'Sign in again.' => text.signInAgain,
    'Sign in again before saving.' => text.signInAgainBeforeSaving,
    'Your session has ended. Sign in again.' => text.sessionEnded,
    'You do not have access to this.' => text.noAccess,
    'This was not found. It may be outside your area.' => text.reportNotFound,
    'Someone else changed this first. Check the timeline and try again.' => text.someoneChangedFirst,
    'The server took too long to answer. Try again.' => text.serverTookTooLong,
    'The server sent an answer the app cannot read.' => text.cannotReadResponse,
    'Enter a full address that starts with http:// or https://' => text.changeAddressFull,
    'The address must start with http:// or https://' => text.changeAddressStart,
    'Leave out anything after ? or #' => text.changeAddressQuery,
    'Notifications are not available here' => text.pushUnavailable,
    'Install a push app to get notifications' => text.pushInstall,
    'Connecting to your push app' => text.pushConnecting,
    'Notifications are on' => text.pushOn,
    'The server cannot send to this phone' => text.pushRefused,
    'Could not reach the server' => text.pushOffline,
    _ => message,
  };
}
