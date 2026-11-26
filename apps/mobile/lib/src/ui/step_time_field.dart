import 'package:flutter/material.dart';

import '../l10n/field_language.dart';
import 'widgets.dart';

/// Lets a field worker record a local date and time when the step is saved.
class StepTimeField extends StatelessWidget {
  const StepTimeField({
    super.key,
    required this.label,
    required this.value,
    required this.onChanged,
    this.allowFuture = false,
  });

  final String label;
  final DateTime? value;
  final ValueChanged<DateTime?>? onChanged;
  final bool allowFuture;

  Future<void> _pick(BuildContext context) async {
    final now = DateTime.now();
    final initial = value?.toLocal() ?? (allowFuture ? now.add(const Duration(hours: 1)) : now);
    final day = await showDatePicker(
      context: context,
      initialDate: initial,
      firstDate: DateTime(now.year - 1),
      lastDate: allowFuture ? now.add(const Duration(days: 30)) : now,
    );
    if (day == null || !context.mounted) return;
    final time = await showTimePicker(context: context, initialTime: TimeOfDay.fromDateTime(initial));
    if (time == null || !context.mounted) return;
    onChanged?.call(DateTime(day.year, day.month, day.day, time.hour, time.minute));
  }

  @override
  Widget build(BuildContext context) => Wrap(
    crossAxisAlignment: WrapCrossAlignment.center,
    spacing: 8,
    children: [
      OutlinedButton.icon(
        icon: const Icon(Icons.schedule),
        onPressed: onChanged == null ? null : () => _pick(context),
        label: Text(value == null ? label : '$label · ${formatTime(value!, context)}'),
      ),
      if (value != null)
        IconButton(
          tooltip: fieldText(context).clearStepTime(label),
          onPressed: onChanged == null ? null : () => onChanged?.call(null),
          icon: const Icon(Icons.clear),
        ),
    ],
  );
}
