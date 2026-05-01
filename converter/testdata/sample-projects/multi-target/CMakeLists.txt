cmake_minimum_required(VERSION 3.20)
project(multi LANGUAGES C VERSION 0.1.0)

# Three targets, three rule types: a STATIC library, a SHARED library
# of the same source set, and a binary linking the static one.
# Exercises the cc_library / cc_binary kind dispatch (Bazel 9 strips
# hdrs from cc_binary; the emitter folds them into srcs).
add_library(multistatic STATIC src/lib.c)
target_include_directories(multistatic PUBLIC
    $<BUILD_INTERFACE:${CMAKE_CURRENT_SOURCE_DIR}/include>
    $<INSTALL_INTERFACE:include>)

add_library(multishared SHARED src/lib.c)
target_include_directories(multishared PUBLIC
    $<BUILD_INTERFACE:${CMAKE_CURRENT_SOURCE_DIR}/include>
    $<INSTALL_INTERFACE:include>)
set_target_properties(multishared PROPERTIES VERSION 0.1.0 SOVERSION 0)

add_executable(multibin src/main.c)
target_link_libraries(multibin PRIVATE multistatic)

install(TARGETS multistatic multishared multibin
        EXPORT multiTargets
        ARCHIVE DESTINATION lib
        LIBRARY DESTINATION lib
        RUNTIME DESTINATION bin)
install(DIRECTORY include/ DESTINATION include)
install(EXPORT multiTargets
        FILE multiTargets.cmake
        NAMESPACE multi::
        DESTINATION lib/cmake/multi)
