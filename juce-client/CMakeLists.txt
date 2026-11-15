cmake_minimum_required(VERSION 3.22)

# One version for the plugin, the helper and the installers: the helper's.
set(CORE_DIR "${CMAKE_CURRENT_LIST_DIR}/../core")
file(STRINGS "${CORE_DIR}/internal/backuper/helper/helper.go" HELPER_VERSION_LINE REGEX "^var Version = ")
string(REGEX MATCH "\"([0-9.]+)\"" _ "${HELPER_VERSION_LINE}")
set(HELPER_VERSION "${CMAKE_MATCH_1}")
if(NOT HELPER_VERSION)
    message(FATAL_ERROR "Could not read the helper version from helper.go")
endif()

project(AudioBackuper VERSION ${HELPER_VERSION})

set(CMAKE_CXX_STANDARD 17)
set(CMAKE_CXX_STANDARD_REQUIRED ON)
set(CMAKE_EXPORT_COMPILE_COMMANDS ON)

option(AUDIO_BACKUPER_INSTALL "Copy the built plugins into the user's plugin folders (macOS)" ON)
option(AUDIO_BACKUPER_TESTS "Build the processor tests" ON)

include(FetchContent)
FetchContent_Declare(
    JUCE
    GIT_REPOSITORY https://github.com/juce-framework/JUCE.git
    GIT_TAG        8.0.4
    GIT_SHALLOW    TRUE
)
FetchContent_MakeAvailable(JUCE)

# ── The helper ────────────────────────────────────────────────────────────────
# A Go program from ../core. It is built here and placed inside every plugin
# bundle, so installing the plugin is all a user has to do.
find_program(GO_EXECUTABLE go REQUIRED)
message(STATUS "Audio Backuper version: ${HELPER_VERSION}")

# Identifiers of the applications registered with the clouds. Without the
# file the build works, but signing in through the browser is unavailable.
set(HELPER_LDFLAGS "-s -w")
set(OAUTH_FILE "${CMAKE_CURRENT_SOURCE_DIR}/oauth.json")
if(EXISTS "${OAUTH_FILE}")
    file(READ "${OAUTH_FILE}" OAUTH_JSON)
    set(AUTH_PKG "github.com/DenisAmell/soundgit/core/internal/backuper/auth")
    foreach(pair "yandexClientId=YandexClientID" "yandexClientSecret=YandexClientSecret"
                 "googleClientId=GoogleClientID" "googleClientSecret=GoogleClientSecret")
        string(REPLACE "=" ";" pair "${pair}")
        list(GET pair 0 key)
        list(GET pair 1 var)
        string(JSON value ERROR_VARIABLE missing GET "${OAUTH_JSON}" "${key}")
        if(NOT missing AND value)
            string(APPEND HELPER_LDFLAGS " -X ${AUTH_PKG}.${var}=${value}")
        endif()
    endforeach()
    message(STATUS "Cloud application identifiers: taken from oauth.json")
else()
    message(STATUS "Cloud application identifiers: none (oauth.json is absent); browser sign-in is disabled")
endif()

set(HELPER_DIR "${CMAKE_BINARY_DIR}/helper")
if(WIN32)
    set(HELPER_NAME "audio-backuper-helper.exe")
else()
    set(HELPER_NAME "audio-backuper-helper")
endif()
set(HELPER "${HELPER_DIR}/${HELPER_NAME}")
set(GO_BUILD ${GO_EXECUTABLE} build -trimpath -ldflags "${HELPER_LDFLAGS}")

if(APPLE)
    # One file for both Intel and Apple Silicon hosts.
    add_custom_target(backuper_helper ALL
        COMMAND ${CMAKE_COMMAND} -E make_directory "${HELPER_DIR}"
        COMMAND ${CMAKE_COMMAND} -E env CGO_ENABLED=0 GOOS=darwin GOARCH=arm64
                ${GO_BUILD} -o "${HELPER_DIR}/helper-arm64" ./cmd/audio-backuper-helper
        COMMAND ${CMAKE_COMMAND} -E env CGO_ENABLED=0 GOOS=darwin GOARCH=amd64
                ${GO_BUILD} -o "${HELPER_DIR}/helper-amd64" ./cmd/audio-backuper-helper
        COMMAND lipo -create -output "${HELPER}" "${HELPER_DIR}/helper-arm64" "${HELPER_DIR}/helper-amd64"
        WORKING_DIRECTORY "${CORE_DIR}"
        BYPRODUCTS "${HELPER}"
        COMMENT "Building the Audio Backuper helper (universal)"
        VERBATIM)
else()
    add_custom_target(backuper_helper ALL
        COMMAND ${CMAKE_COMMAND} -E make_directory "${HELPER_DIR}"
        COMMAND ${CMAKE_COMMAND} -E env CGO_ENABLED=0 ${GO_BUILD} -o "${HELPER}" ./cmd/audio-backuper-helper
        WORKING_DIRECTORY "${CORE_DIR}"
        BYPRODUCTS "${HELPER}"
        COMMENT "Building the Audio Backuper helper"
        VERBATIM)
endif()

# ── The plugin ────────────────────────────────────────────────────────────────
if(APPLE)
    set(PLUGIN_FORMATS AU VST3 Standalone)
else()
    set(PLUGIN_FORMATS VST3 Standalone)
endif()

juce_add_plugin(AudioBackuper
    COMPANY_NAME             "rozetki"
    BUNDLE_ID                "com.rozetki.audiobackuper"
    PLUGIN_MANUFACTURER_CODE "YrSt"
    PLUGIN_CODE              "AbkR"
    FORMATS                  ${PLUGIN_FORMATS}
    PRODUCT_NAME             "Audio Backuper"

    # An effect that leaves the sound alone.
    IS_SYNTH                 FALSE
    NEEDS_MIDI_INPUT         FALSE
    NEEDS_MIDI_OUTPUT        FALSE
    IS_MIDI_EFFECT           FALSE
    EDITOR_WANTS_KEYBOARD_FOCUS TRUE

    AU_MAIN_TYPE             kAudioUnitType_Effect
    VST3_CATEGORIES          "Fx" "Tools"

    # Installed by our own step below, after the helper is inside the bundle.
    COPY_PLUGIN_AFTER_BUILD  FALSE
)

set(PLUGIN_SOURCES
    Source/PluginProcessor.cpp
    Source/PluginEditor.cpp
    Source/HelperClient.cpp)

set(PLUGIN_DEFINITIONS
    JUCE_WEB_BROWSER=0
    JUCE_USE_CURL=0
    JUCE_VST3_CAN_REPLACE_VST2=0
    JUCE_DISPLAY_SPLASH_SCREEN=0
    AUDIO_BACKUPER_HELPER_VERSION="${HELPER_VERSION}")

target_sources(AudioBackuper PRIVATE ${PLUGIN_SOURCES})
target_include_directories(AudioBackuper PRIVATE Source)
target_compile_definitions(AudioBackuper PUBLIC ${PLUGIN_DEFINITIONS})
if(MSVC)
    target_compile_options(AudioBackuper PUBLIC /utf-8)
endif()

target_link_libraries(AudioBackuper
    PRIVATE
        juce::juce_audio_utils
    PUBLIC
        juce::juce_recommended_config_flags
        juce::juce_recommended_lto_flags
        juce::juce_recommended_warning_flags
)

# Put the helper into each bundle, sign, and (on macOS) install for the user.
foreach(format ${PLUGIN_FORMATS})
    set(target "AudioBackuper_${format}")
    if(NOT TARGET ${target})
        continue()
    endif()
    add_dependencies(${target} backuper_helper)
    get_target_property(artefact ${target} JUCE_PLUGIN_ARTEFACT_FILE)

    if(APPLE)
        add_custom_command(TARGET ${target} POST_BUILD
            COMMAND ${CMAKE_COMMAND} -E make_directory "${artefact}/Contents/Resources"
            COMMAND ${CMAKE_COMMAND} -E copy "${HELPER}" "${artefact}/Contents/Resources/${HELPER_NAME}"
            COMMAND codesign --force --sign - "${artefact}/Contents/Resources/${HELPER_NAME}"
            COMMAND codesign --force --sign - "${artefact}"
            COMMENT "Embedding the helper into ${format}"
            VERBATIM)
        if(AUDIO_BACKUPER_INSTALL AND NOT format STREQUAL "Standalone")
            if(format STREQUAL "AU")
                set(destination "$ENV{HOME}/Library/Audio/Plug-Ins/Components/Audio Backuper.component")
            else()
                set(destination "$ENV{HOME}/Library/Audio/Plug-Ins/VST3/Audio Backuper.vst3")
            endif()
            add_custom_command(TARGET ${target} POST_BUILD
                COMMAND ${CMAKE_COMMAND} -E rm -rf "${destination}"
                COMMAND ditto "${artefact}" "${destination}"
                COMMENT "Installing ${format} for the current user"
                VERBATIM)
        endif()
    elseif(format STREQUAL "VST3")
        add_custom_command(TARGET ${target} POST_BUILD
            COMMAND ${CMAKE_COMMAND} -E make_directory "${artefact}/Contents/Resources"
            COMMAND ${CMAKE_COMMAND} -E copy "${HELPER}" "${artefact}/Contents/Resources/${HELPER_NAME}"
            COMMENT "Embedding the helper into ${format}"
            VERBATIM)
    endif()
endforeach()

# ── Tests ─────────────────────────────────────────────────────────────────────
if(AUDIO_BACKUPER_TESTS)
    juce_add_console_app(AudioBackuperTests PRODUCT_NAME "AudioBackuperTests")
    target_sources(AudioBackuperTests PRIVATE Tests/ProcessorTests.cpp ${PLUGIN_SOURCES})
    target_include_directories(AudioBackuperTests PRIVATE Source)
    target_compile_definitions(AudioBackuperTests PRIVATE ${PLUGIN_DEFINITIONS} JUCE_MODAL_LOOPS_PERMITTED=1)
    if(MSVC)
        target_compile_options(AudioBackuperTests PRIVATE /utf-8)
    endif()
    target_link_libraries(AudioBackuperTests
        PRIVATE
            juce::juce_audio_utils
            juce::juce_recommended_config_flags
            juce::juce_recommended_warning_flags)
    enable_testing()
    add_test(NAME processor COMMAND AudioBackuperTests)
endif()
