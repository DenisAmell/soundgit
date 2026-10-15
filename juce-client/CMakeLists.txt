cmake_minimum_required(VERSION 3.22)
project(AudioBackuper VERSION 1.0.0)

set(CMAKE_CXX_STANDARD 17)
set(CMAKE_EXPORT_COMPILE_COMMANDS ON)


include(FetchContent)
FetchContent_Declare(
    JUCE
    GIT_REPOSITORY https://github.com/juce-framework/JUCE.git
    GIT_TAG        8.0.4
    GIT_SHALLOW    TRUE
)
FetchContent_MakeAvailable(JUCE)

juce_add_plugin(AudioBackuper
    # ── Метаданные плагина ────────────────────────────────────────────────────
    COMPANY_NAME            "rozetki"
    PLUGIN_MANUFACTURER_CODE "YrSt"
    PLUGIN_CODE             "AbkR"
    FORMATS                 AU VST3 Standalone

    PRODUCT_NAME            "Audio Backuper"

    # ── Тип: эффект, но без реальной обработки ───────────────────────────────
    IS_SYNTH                FALSE
    NEEDS_MIDI_INPUT        FALSE
    NEEDS_MIDI_OUTPUT       FALSE
    IS_MIDI_EFFECT          FALSE
    EDITOR_WANTS_KEYBOARD_FOCUS FALSE

    # ── AU специфика (для Logic Pro) ─────────────────────────────────────────
    AU_MAIN_TYPE            kAudioUnitType_Effect

    VST3_CATEGORIES         "Fx" "Tools"

    COPY_PLUGIN_AFTER_BUILD TRUE
)

# ── Исполняемый файл ───────────────────────────────────────────────────────────
# add_executable(AudioBackuper
#     src/Main.cpp
# )

target_sources(AudioBackuper
    PRIVATE
        Source/PluginProcessor.cpp
        Source/PluginEditor.cpp
        Source/BackuperService.cpp
        Source/HttpClient.h
)

target_include_directories(AudioBackuper
    PRIVATE
        Source
)

target_compile_definitions(AudioBackuper
    PUBLIC
        JUCE_WEB_BROWSER=0
        JUCE_USE_CURL=0
        JUCE_VST3_CAN_REPLACE_VST2=0
        JUCE_DISPLAY_SPLASH_SCREEN=0
)


target_link_libraries(AudioBackuper
    PRIVATE
        juce::juce_audio_utils
        juce::juce_gui_extra
    PUBLIC
        juce::juce_recommended_config_flags
        juce::juce_recommended_lto_flags
        juce::juce_recommended_warning_flags
)



# # ── Линкуем только нужные модули JUCE ─────────────────────────────────────────
# target_link_libraries(AudioBackuper
#     PRIVATE
#         juce::juce_core          # String, File, Timer, JSON, ...
#         juce::juce_events        # MessageManager, async
#         juce::juce_gui_basics    # Component, Graphics, ...
#         juce::juce_gui_extra     # DocumentWindow, ...
# )

# target_compile_definitions(JuceMinimal
#     PRIVATE
#         JUCE_STANDALONE_APPLICATION=1
#         JUCE_WEB_BROWSER=0
#         JUCE_USE_CURL=0
#         JUCE_DISPLAY_SPLASH_SCREEN=0
# )
