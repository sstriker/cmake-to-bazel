cmake_minimum_required(VERSION 3.20)
project(L36 LANGUAGES C VERSION 0.1.0)

find_package(L16 CONFIG REQUIRED)
find_package(L19 CONFIG REQUIRED)

add_library(L36 STATIC src/L36.c)
target_include_directories(L36 PUBLIC
    $<BUILD_INTERFACE:${CMAKE_CURRENT_SOURCE_DIR}/include>
    $<INSTALL_INTERFACE:include>)
target_link_libraries(L36 PUBLIC L16::L16 L19::L19)

install(TARGETS L36
        EXPORT L36Targets
        ARCHIVE DESTINATION lib)
install(EXPORT L36Targets
        FILE L36Targets.cmake
        NAMESPACE L36::
        DESTINATION lib/cmake/L36)
